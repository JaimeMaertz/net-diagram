package netdiagram

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JaimeMaertz/net-diagram/internal/ciscossh"
)

// Collect logs into a switch and runs its vendor's commands, returning each command's output.
// Only allowlisted commands are sent. Session setup is the one exception, and it changes no
// configuration: Netgear's "enable", and the "terminal datadump" ciscossh sends to stop Cisco's
// paging for that session.
func Collect(vendor, host, user, password string) (map[string]string, error) {
	switch vendor {
	case "Netgear":
		s, err := DialTelnet(host, user, password, "")
		if err != nil {
			return nil, err
		}
		defer s.Close()
		return CollectNetgear(s.Run)
	case "Cisco":
		s, err := ciscossh.Dial(host, user, password)
		if err != nil {
			return nil, err
		}
		defer s.Close()
		return CollectCisco(func(cmd string) (string, error) {
			if !Allowed(cmd) {
				return "", fmt.Errorf("refused to send %q: not on the read-only allowlist", cmd)
			}
			return s.Run(cmd)
		})
	case "TP-Link":
		s, err := dialTPLink(host, user, password)
		if err != nil {
			return nil, err
		}
		defer s.Close()
		return CollectTPLink(s.Run)
	}
	return nil, fmt.Errorf("%s switches can't be read yet", vendor)
}

// dialTPLink logs into a TP-Link JetStream switch over SSH, else telnet. Telnet is tried only when
// SSH can't be used at all (closed, or a handshake this client can't complete), never after a refused
// password, so a wrong login costs one attempt. Some switches offer SSH with only a 512-bit DSA host
// key, which no current SSH client accepts (seen on a TL-SG3428X 1.0 with firmware 1.0.7, while another
// on the same firmware offered none); such a switch is read over telnet.
func dialTPLink(host, user, password string) (*CLISession, error) {
	s, sshErr := DialSSHCLI(host, user, password, "")
	if sshErr == nil {
		return s, nil
	}
	if errors.Is(sshErr, ErrAuth) {
		return nil, sshErr
	}
	s, telErr := DialTelnet(host, user, password, "")
	if telErr == nil {
		return s, nil
	}
	if errors.Is(telErr, ErrAuth) {
		return nil, telErr
	}
	why := sshErr.Error()
	if strings.Contains(why, "DSA key size") {
		why = "the switch's SSH offers only a 512-bit DSA host key, which can't be used (turn telnet on to read it)"
	}
	return nil, fmt.Errorf("SSH: %s; telnet: %v", why, telErr)
}

// Parse builds a Switch from a vendor's command outputs.
func Parse(vendor, host string, out map[string]string) (Switch, error) {
	switch vendor {
	case "Netgear":
		return ParseNetgear(host, out), nil
	case "Cisco":
		return ParseCisco(host, out)
	case "TP-Link":
		return ParseTPLink(host, out)
	}
	return Switch{}, fmt.Errorf("no parser for %s", vendor)
}

// VendorOf guesses a switch's vendor from saved outputs: Netgear's "show sysinfo" or Cisco's
// "show system".
func VendorOf(out map[string]string) string {
	if _, ok := out["show sysinfo"]; ok {
		return "Netgear"
	}
	if _, ok := out["show system"]; ok {
		return "Cisco"
	}
	if _, ok := out["show system-info"]; ok {
		return "TP-Link"
	}
	return ""
}

var anyPortWord = regexp.MustCompile(`^\S+\d+/\d+(/\d+)?$`)

// SaveOutputs writes each command's output to dir as one file, named for the command with spaces
// as "_" and a port's slashes as "-" ("show lldp neighbors gi1/0/5" →
// show_lldp_neighbors_gi1-0-5.txt). LoadOutputs reads them back.
func SaveOutputs(dir string, outputs map[string]string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for c, o := range outputs {
		name := strings.NewReplacer(" ", "_", "/", "-").Replace(c) + ".txt"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(o), 0o644); err != nil {
			return err
		}
	}
	return nil
}
