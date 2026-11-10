// Package ciscossh runs read-only CLI commands on a Cisco small-business switch over SSH, the way a
// person would type them.
//
// A Switch is one SSH session with an interactive shell: it logs in (answering the shell's own
// "User Name:" / "Password:" prompts if the switch asks for them inside the session), turns paging off,
// and then runs one command at a time, waiting for the prompt to come back. A command's output is
// checked for the switch's error lines, so a refused command is an error, not text to read. Written
// against Cisco's small-business CLI (Catalyst 1300, CBS350); the prompt and error patterns are the only
// parts that are switch-specific.
package ciscossh

import (
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrAuth is returned when the switch refuses the username and password.
var ErrAuth = errors.New("ciscossh: login refused (wrong username or password)")

var (
	// A prompt at the very end of the output: "C1300#", "C1300(config)#", "C1300(config-if)#", "C1300>".
	promptRe = regexp.MustCompile(`(?:^|\n)[A-Za-z0-9._\-]+(?:\([^)\n]*\))?[#>] ?$`)
	// A question the switch waits on: "Overwrite file [startup-config].... (Y/N)[N] ?"
	confirmRe = regexp.MustCompile(`\(Y/N\)\s*\[[YN]\]\s*\??\s*$`)
	// The pager, if paging is still on.
	moreRe     = regexp.MustCompile(`More: <space>[^\n]*$`)
	userRe     = regexp.MustCompile(`(?i)user ?name:\s*$`)
	passwordRe = regexp.MustCompile(`(?i)password:\s*$`)
	// "% Unrecognized command", "% bad parameter value", and console messages of severity
	// emergency to error ("%AAA-E-..."). Info and notice messages ("%COPY-I-FILECPY") are not errors.
	errorRe = regexp.MustCompile(`^\s*(%\s|%[A-Z0-9_]+-[FACE]-)`)
	escRe   = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|.\x08`)
)

// Switch is one switch, logged in. It is not safe for use from several goroutines.
type Switch struct {
	Host    string
	Timeout time.Duration // how long one command may take before its prompt comes back (default 30 s)
	Prompt  string        // the prompt seen at login, e.g. "C1300#"

	client *ssh.Client
	sess   *ssh.Session
	in     io.WriteCloser
	chunks chan []byte
	buf    string
}

// CommandError is a command the switch refused, with the lines that said so.
type CommandError struct {
	Command string
	Lines   []string
	Output  string
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("%q refused: %s", e.Command, strings.Join(e.Lines, " / "))
}

// Dial logs in to host (port 22 unless host names one) and turns paging off.
func Dial(host, user, password string) (*Switch, error) {
	addr := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		addr = net.JoinHostPort(host, "22")
	}
	// The switch runs an old OpenSSH, so the older key exchanges and host key types are offered too.
	supported, insecure := ssh.SupportedAlgorithms(), ssh.InsecureAlgorithms()
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = password
				}
				return answers, nil
			}),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
		Config: ssh.Config{
			KeyExchanges: append(supported.KeyExchanges, insecure.KeyExchanges...),
			Ciphers:      append(supported.Ciphers, insecure.Ciphers...),
			MACs:         append(supported.MACs, insecure.MACs...),
		},
		HostKeyAlgorithms: append(supported.HostKeys, insecure.HostKeys...),
	}
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, ErrAuth
		}
		return nil, err
	}
	s := &Switch{Host: host, Timeout: 30 * time.Second, client: client, chunks: make(chan []byte, 64)}
	if err := s.shell(user, password); err != nil {
		client.Close()
		return nil, err
	}
	return s, nil
}

func (s *Switch) shell(user, password string) error {
	sess, err := s.client.NewSession()
	if err != nil {
		return err
	}
	s.sess = sess
	if err := sess.RequestPty("vt100", 0, 512, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		return err
	}
	if s.in, err = sess.StdinPipe(); err != nil {
		return err
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	if err := sess.Shell(); err != nil {
		return err
	}
	go func() {
		defer close(s.chunks)
		for {
			b := make([]byte, 4096)
			n, err := out.Read(b)
			if n > 0 {
				s.chunks <- b[:n]
			}
			if err != nil {
				return
			}
		}
	}()

	// Some switches ask for the user again inside the session, whatever SSH already sent.
	for tries := 0; tries < 4; tries++ {
		text, which, err := s.expect(15*time.Second, promptRe, userRe, passwordRe)
		if err != nil {
			return fmt.Errorf("ciscossh: no prompt after login: %w", err)
		}
		switch which {
		case 0:
			lines := strings.Split(strings.TrimRight(text, " "), "\n")
			s.Prompt = strings.TrimSpace(lines[len(lines)-1])
			if strings.HasSuffix(s.Prompt, ">") {
				return fmt.Errorf("ciscossh: logged in at %q, not privileged (#): the user needs privilege 15", s.Prompt)
			}
			if _, err := s.Run("terminal datadump"); err != nil {
				return fmt.Errorf("ciscossh: turning paging off: %w", err)
			}
			return nil
		case 1:
			s.send(user)
		case 2:
			if tries > 1 {
				return ErrAuth
			}
			s.send(password)
		}
	}
	return ErrAuth
}

func (s *Switch) send(line string) error {
	_, err := io.WriteString(s.in, line+"\r")
	return err
}

// expect reads until the output so far ends with one of res, and returns that output and which matched.
func (s *Switch) expect(timeout time.Duration, res ...*regexp.Regexp) (string, int, error) {
	deadline := time.After(timeout)
	for {
		for i, re := range res {
			if re.MatchString(s.buf) {
				text := s.buf
				s.buf = ""
				return text, i, nil
			}
		}
		select {
		case b, ok := <-s.chunks:
			if !ok {
				return s.buf, -1, errors.New("connection closed by the switch")
			}
			s.buf += clean(string(b))
		case <-deadline:
			return s.buf, -1, fmt.Errorf("timed out after %s; last output: %q", timeout, tail(s.buf, 200))
		}
	}
}

func clean(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	for escRe.MatchString(s) {
		s = escRe.ReplaceAllString(s, "")
	}
	return s
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// Run sends one command and returns its output (without the echoed command and the prompt after it).
// A line the switch marks as an error makes it a *CommandError. A Y/N question is always answered N
// and is an error too: nothing this package sends is meant to ask one.
func (s *Switch) Run(command string) (string, error) {
	// Anything the switch printed on its own since the last command (a console message) is not this
	// command's output.
	s.drain()
	if err := s.send(command); err != nil {
		return "", err
	}
	var out strings.Builder
	var asked string
	for {
		text, which, err := s.expect(s.Timeout, promptRe, confirmRe, moreRe)
		out.WriteString(text)
		if err != nil {
			return output(out.String(), command), fmt.Errorf("%q: %w", command, err)
		}
		switch which {
		case 1:
			lines := strings.Split(strings.TrimSpace(text), "\n")
			asked = lines[len(lines)-1]
			s.send("N")
			continue
		case 2:
			io.WriteString(s.in, " ")
			continue
		}
		break
	}
	text := output(out.String(), command)
	if asked != "" {
		return text, &CommandError{Command: command, Lines: []string{"asked " + asked + " (answered N)"}, Output: text}
	}
	var bad []string
	for _, line := range strings.Split(text, "\n") {
		if errorRe.MatchString(line) {
			bad = append(bad, strings.TrimSpace(line))
		}
	}
	if len(bad) > 0 {
		return text, &CommandError{Command: command, Lines: bad, Output: text}
	}
	return text, nil
}

func (s *Switch) drain() {
	for {
		select {
		case b, ok := <-s.chunks:
			if !ok {
				return
			}
			s.buf += clean(string(b))
		default:
			s.buf = ""
			return
		}
	}
}

// output drops the echoed command (first line), the prompt (last line) and pager lines.
func output(text, command string) string {
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && strings.Contains(lines[0], strings.TrimSpace(command)) {
		lines = lines[1:]
	}
	if len(lines) > 0 && promptRe.MatchString("\n"+lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	kept := lines[:0]
	for _, l := range lines {
		if !strings.Contains(l, "More: <space>") {
			kept = append(kept, l)
		}
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n ")
}

// Close logs out.
func (s *Switch) Close() error {
	if s.in != nil {
		s.send("exit")
	}
	if s.sess != nil {
		s.sess.Close()
	}
	return s.client.Close()
}
