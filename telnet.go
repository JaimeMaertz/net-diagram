package netdiagram

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// Telnet option negotiation bytes (RFC 854/857/858).
const (
	iac  = 255
	dont = 254
	do   = 253
	wont = 252
	will = 251
	sb   = 250
	se   = 240

	optEcho = 1
	optSGA  = 3
)

// pager matches a CLI's "more" prompt, answered with a space.
var pager = regexp.MustCompile(`(?i)--more--( or \(q\)uit)?|\(q\)uit`)

// TelnetSession is a logged-in telnet CLI session that can only send allowlisted commands.
type TelnetSession struct {
	conn   net.Conn
	Prompt string // the privileged prompt as last seen, e.g. "(M4250-10G2F-PoE+) #"
}

// ErrAuth means the switch refused the login.
var ErrAuth = errors.New("login refused")

// DialTelnet logs in and enters privileged mode. The password is never logged or returned.
// enablePassword may be empty; it is sent only if the switch asks for one after "enable".
func DialTelnet(host, user, password, enablePassword string) (*TelnetSession, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, "23"), 5*time.Second)
	if err != nil {
		return nil, err
	}
	s := &TelnetSession{conn: conn}
	fail := func(err error) (*TelnetSession, error) { conn.Close(); return nil, err }

	if _, err := s.readUntil(regexp.MustCompile(`(?i)(user( ?name)?|login):\s*$`), 10*time.Second); err != nil {
		return fail(fmt.Errorf("no user prompt: %w", err))
	}
	s.send(user)
	if _, err := s.readUntil(regexp.MustCompile(`(?i)password:\s*$`), 10*time.Second); err != nil {
		return fail(fmt.Errorf("no password prompt: %w", err))
	}
	s.send(password)
	out, err := s.readUntil(regexp.MustCompile(`(?i)([>#]\s*$)|(user( ?name)?|login):\s*$|incorrect|failed|denied`), 10*time.Second)
	if err != nil {
		return fail(fmt.Errorf("no prompt after login: %w", err))
	}
	if !strings.HasSuffix(strings.TrimRight(out, " "), ">") && !strings.HasSuffix(strings.TrimRight(out, " "), "#") {
		return fail(ErrAuth)
	}
	if strings.HasSuffix(strings.TrimRight(out, " "), ">") {
		s.send("enable")
		out, err = s.readUntil(regexp.MustCompile(`(?i)([>#]\s*$)|password:\s*$`), 10*time.Second)
		if err != nil {
			return fail(fmt.Errorf("enable: %w", err))
		}
		if strings.HasSuffix(strings.TrimRight(out, " "), ":") {
			s.send(enablePassword)
			out, err = s.readUntil(regexp.MustCompile(`[>#]\s*$`), 10*time.Second)
			if err != nil {
				return fail(fmt.Errorf("enable password: %w", err))
			}
		}
		if !strings.HasSuffix(strings.TrimRight(out, " "), "#") {
			return fail(errors.New("enable did not reach the privileged # prompt"))
		}
	}
	s.Prompt = lastLine(out)
	return s, nil
}

// Run sends one allowlisted command and returns its output without the echo and the prompt.
// Pager prompts are answered with a space and removed from the output.
func (s *TelnetSession) Run(cmd string) (string, error) {
	if !Allowed(cmd) {
		return "", fmt.Errorf("refused to send %q: not on the read-only allowlist", cmd)
	}
	s.send(cmd)
	out, err := s.readUntil(regexp.MustCompile(regexp.QuoteMeta(strings.TrimSpace(s.Prompt))+`\s*$`), 60*time.Second)
	if err != nil {
		return out, err
	}
	out = pager.ReplaceAllString(out, "")
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		// A bare carriage return moves back to the start of the line, as a terminal would: the
		// switch erases its pager prompt with spaces and a CR, so keep only what follows the last one.
		l = strings.TrimSuffix(l, "\r")
		if j := strings.LastIndex(l, "\r"); j >= 0 {
			l = l[j+1:]
		}
		lines[i] = l
	}
	if len(lines) > 0 && strings.Contains(lines[0], strings.TrimSpace(cmd)) {
		lines = lines[1:] // the echo
	}
	if len(lines) > 0 {
		lines = lines[:len(lines)-1] // the prompt
	}
	return strings.Join(lines, "\n"), nil
}

// Close logs out.
func (s *TelnetSession) Close() error {
	s.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	s.conn.Write([]byte("logout\r\n"))
	return s.conn.Close()
}

func (s *TelnetSession) send(line string) {
	s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	s.conn.Write([]byte(line + "\r\n"))
}

// readUntil reads until want matches the end of the text received so far, answering telnet
// negotiation and pager prompts along the way.
func (s *TelnetSession) readUntil(want *regexp.Regexp, timeout time.Duration) (string, error) {
	var text bytes.Buffer
	buf := make([]byte, 4096)
	deadline := time.Now().Add(timeout)
	for {
		s.conn.SetReadDeadline(deadline)
		n, err := s.conn.Read(buf)
		if n > 0 {
			text.Write(s.negotiate(buf[:n]))
			tail := text.Bytes()
			if len(tail) > 200 {
				tail = tail[len(tail)-200:]
			}
			if pager.Match(tail) && !want.Match(tail) {
				s.conn.Write([]byte(" "))
				// Drop the pager prompt so the next match looks at fresh text.
				t := pager.ReplaceAllString(text.String(), "")
				text.Reset()
				text.WriteString(t)
				continue
			}
			if want.MatchString(strings.TrimRight(text.String(), "\x00")) {
				return text.String(), nil
			}
		}
		if err != nil {
			return text.String(), err
		}
	}
}

// negotiate strips telnet commands from b and answers them: the switch may echo and suppress
// go-ahead; every other option is refused.
func (s *TelnetSession) negotiate(b []byte) []byte {
	var out []byte
	for i := 0; i < len(b); i++ {
		if b[i] != iac || i+1 >= len(b) {
			out = append(out, b[i])
			continue
		}
		cmd := b[i+1]
		switch {
		case cmd == iac:
			out = append(out, iac)
			i++
		case (cmd == do || cmd == dont || cmd == will || cmd == wont) && i+2 < len(b):
			opt := b[i+2]
			var reply byte
			switch cmd {
			case do:
				reply = wont
				if opt == optSGA {
					reply = will
				}
			case will:
				reply = dont
				if opt == optEcho || opt == optSGA {
					reply = do
				}
			}
			if reply != 0 {
				s.conn.Write([]byte{iac, reply, opt})
			}
			i += 2
		case cmd == sb:
			for i < len(b) && !(b[i] == iac && i+1 < len(b) && b[i+1] == se) {
				i++
			}
			i++
		default:
			i++
		}
	}
	return out
}

func lastLine(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}
