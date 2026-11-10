package netdiagram

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JaimeMaertz/netdiagram/internal/ciscossh"
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
	}
	return nil, fmt.Errorf("%s switches can't be read yet", vendor)
}

// Parse builds a Switch from a vendor's command outputs.
func Parse(vendor, host string, out map[string]string) (Switch, error) {
	switch vendor {
	case "Netgear":
		return ParseNetgear(host, out), nil
	case "Cisco":
		return ParseCisco(host, out)
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
