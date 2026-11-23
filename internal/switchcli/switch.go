// Package switchcli runs read-only CLI commands on a network switch, over SSH or telnet, the way a person
// would type them. It has no way to enter configuration mode, save, or answer yes to a question.
//
// A Switch is one interactive CLI session: it logs in (answering the CLI's own "User:" / "Password:"
// prompts if the switch asks for them inside the session), reaches the privileged prompt, turns paging off,
// and then runs one command at a time, waiting for the prompt to come back. A command's output is checked
// for the switch's error lines, so a refused command is an error, not text to read. A command that asks a
// question is answered no. What differs between switch families (prompts, error lines, questions) is a
// Family: Cisco (Catalyst 1300, CBS350, SG250) and Netgear (M4250, M4350).
//
// A prompt is told from text that only looks like one (a login banner's "SITE#" or "login:" line, the
// banner again inside show running-config) by what follows it: the switch stops at a prompt and waits.
// At login a match counts once the output has gone quiet, and the prompt is confirmed by an empty line
// bringing the same prompt back; after login a prompt that starts with the session's own name counts at
// once, and anything else, or any show command's output, only once quiet.
package switchcli

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
var ErrAuth = errors.New("switchcli: login refused (wrong username or password)")

// Family is what one switch family's CLI looks like.
type Family struct {
	Name      string
	Transport string         // "ssh" or "telnet", when the caller names none
	Prompt    *regexp.Regexp // a prompt at the very end of the output
	Confirm   *regexp.Regexp // a question the switch waits on
	No        string         // the answer to Confirm (never yes)
	More      *regexp.Regexp // the pager, if paging is still on (answered with a space)
	Error     *regexp.Regexp // a line that means the command was refused
	PagingOff string         // sent once after login
	Enable    bool           // the login lands at ">" and "enable" reaches the privileged prompt
}

var (
	// Cisco's small-business CLI (Catalyst 1300, CBS350, SG250), measured on two C1300s; the SG250's from its
	// CLI guide (Sx250 2.4.5), not yet a unit.
	Cisco = &Family{
		Name:      "Cisco",
		Transport: "ssh",
		// "C1300#", "C1300(config)#", "C1300(config-if)#", "C1300>". After a Y/N question is answered, the switch
		// can print the prompt twice on one line ("SW(config)#SW(config)#").
		Prompt: regexp.MustCompile(`(?:^|\n)(?:[A-Za-z0-9._\-]+(?:\([^)\n]*\))?[#>] ?)+$`),
		// "Overwrite file [startup-config].... (Y/N)[N] ?" (C1300); "Overwrite file [startup-config] ?[Yes/press
		// any key for no]...." (SG250, Sx250 CLI guide p. 351).
		Confirm: regexp.MustCompile(`\(Y/N\)\s*\[[YN]\]\s*\??\s*$|\[Yes/press any key for no\]\.*\s*$`),
		No:      "N",
		More:    regexp.MustCompile(`More: <space>[^\n]*$`),
		// "% Unrecognized command", "% bad parameter value", and console messages of severity emergency to error
		// ("%AAA-E-..."). Info and notice messages ("%COPY-I-FILECPY") are not errors.
		Error:     regexp.MustCompile(`^\s*(%\s|%[A-Z0-9_]+-[FACE]-)`),
		PagingOff: "terminal datadump",
	}

	// Netgear's fully managed CLI (M4250, M4350), measured on an M4250-10G2F-PoE+ (firmware 13.0.6.8). Telnet
	// is on and SSH off as the switch comes.
	Netgear = &Family{
		Name:      "Netgear",
		Transport: "telnet",
		// "(M4250-10G2F-PoE+) >", "(M4250-10G2F-PoE+)#", "(M4250-10G2F-PoE+)(Config)#", "(…)(Interface 0/1)#",
		// "(…)(Vlan)#".
		Prompt: regexp.MustCompile(`(?:^|\n)\([^()\n]+\) ?(?:\([^()\n]*\))? ?[#>] ?$`),
		// "Are you sure you want to save? (y/n) "
		Confirm: regexp.MustCompile(`(?i)\(y/n\)\s*$`),
		No:      "n",
		More:    regexp.MustCompile(`--More-- or \(q\)uit *$`),
		// "% Invalid input detected at '^' marker.", "% Unrecognized command", "Command not found / Incomplete
		// command. Use ? to list commands.", "Incorrect input! Use 'vlan  <vlan-range | all>'.", "Invalid input.
		// Please specify an integer in the range 1 to 4093."
		Error:     regexp.MustCompile(`^\s*(%\s|Command not found|Incorrect input|Invalid input|Error[:!]|ERROR:|Couldn't )`),
		PagingOff: "terminal length 0",
		Enable:    true,
	}

	userRe     = regexp.MustCompile(`(?i)(user( ?name)?|login):\s*$`)
	passwordRe = regexp.MustCompile(`(?i)password:\s*$`)
	escRe      = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|.\x08`)
)

// Switch is one switch, logged in. It is not safe for use from several goroutines.
type Switch struct {
	Host    string
	Family  *Family
	Timeout time.Duration // how long one command may take before its prompt comes back (default 30 s)
	Prompt  string        // the privileged prompt seen at login, e.g. "C1300#"
	base    string        // what every prompt of this session starts with: the name, "C1300" or "(M4250-10G2F-PoE+)"
	current string        // the prompt after the last command
	typedAt string        // the prompt the last command was typed at

	close  func() error
	in     io.Writer
	eol    string // what ends a typed line: "\r" on SSH, "\r\n" on telnet
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

// Options says how to reach a switch.
type Options struct {
	Host           string // address, with a port if not the transport's usual one
	User, Password string
	EnablePassword string  // sent only if "enable" asks for one
	Family         *Family // nil is Cisco
	Transport      string  // "ssh" or "telnet"; "" is the family's
}

// Dial logs in to a Cisco switch over SSH (port 22 unless host names one) and turns paging off.
func Dial(host, user, password string) (*Switch, error) {
	return Open(Options{Host: host, User: user, Password: password, Family: Cisco, Transport: "ssh"})
}

// Open logs in as o says, reaches the privileged prompt and turns paging off.
func Open(o Options) (*Switch, error) {
	f := o.Family
	if f == nil {
		f = Cisco
	}
	transport := o.Transport
	if transport == "" {
		transport = f.Transport
	}
	s := &Switch{Host: o.Host, Family: f, Timeout: 30 * time.Second, chunks: make(chan []byte, 64)}
	var err error
	switch transport {
	case "ssh":
		err = s.dialSSH(o.Host, o.User, o.Password)
	case "telnet":
		err = s.dialTelnet(o.Host)
	default:
		return nil, fmt.Errorf("switchcli: no transport %q (ssh or telnet)", transport)
	}
	if err != nil {
		return nil, err
	}
	if err := s.login(o.User, o.Password, o.EnablePassword); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *Switch) dialSSH(host, user, password string) error {
	addr := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		addr = net.JoinHostPort(host, "22")
	}
	// Switches run old SSH servers, so the older key exchanges and host key types are offered too.
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
			return ErrAuth
		}
		return err
	}
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return err
	}
	s.close = func() error { sess.Close(); return client.Close() }
	if err := sess.RequestPty("vt100", 0, 512, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		s.close()
		return err
	}
	in, err := sess.StdinPipe()
	if err != nil {
		s.close()
		return err
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		s.close()
		return err
	}
	if err := sess.Shell(); err != nil {
		s.close()
		return err
	}
	s.in, s.eol = in, "\r"
	go s.pump(out, nil)
	return nil
}

func (s *Switch) dialTelnet(host string) error {
	addr := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		addr = net.JoinHostPort(host, "23")
	}
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return err
	}
	s.in, s.eol, s.close = conn, "\r\n", conn.Close
	go s.pump(conn, func(b []byte) []byte { return negotiate(conn, b) })
	return nil
}

// pump copies what the switch sends into s.chunks, through filter (telnet negotiation) if set.
func (s *Switch) pump(r io.Reader, filter func([]byte) []byte) {
	defer close(s.chunks)
	for {
		b := make([]byte, 4096)
		n, err := r.Read(b)
		if n > 0 {
			c := b[:n]
			if filter != nil {
				c = filter(c)
			}
			s.chunks <- c
		}
		if err != nil {
			return
		}
	}
}

// loginQuiet is how long the output must stay quiet after something that looks like a prompt before it
// counts, at login: a banner sent in one go arrives well inside it.
const loginQuiet = 300 * time.Millisecond

// loginStall is how long login waits on output that matches nothing before pressing Enter: a long banner
// can stop for it ("After 510 characters, press <Enter> to continue", 250 Series admin guide p. 48).
var loginStall = 4 * time.Second

func (s *Switch) login(user, password, enablePassword string) error {
	sentPassword := false
	quiet := func(string, int) time.Duration { return loginQuiet }
	nudges := 0
	for tries := 0; tries < 8; tries++ {
		text, which, err := s.expectSettled(loginStall, quiet, s.Family.Prompt, userRe, passwordRe)
		if err != nil {
			if which == -1 && nudges < 2 && !errors.Is(err, errClosed) {
				nudges++
				s.send("") // a banner waiting for Enter; at a question it's only asked again
				continue
			}
			return fmt.Errorf("switchcli: no prompt after login: %w", err)
		}
		switch which {
		case 0:
			prompt, err := s.confirmPrompt(lastLine(text))
			if err != nil {
				return err
			}
			if strings.HasSuffix(prompt, ">") {
				if !s.Family.Enable {
					return fmt.Errorf("switchcli: logged in at %q, not privileged (#): the user needs privilege 15", prompt)
				}
				if prompt, err = s.enable(enablePassword); err != nil {
					return err
				}
			}
			s.Prompt, s.current, s.base = prompt, prompt, promptBase(prompt)
			if s.Family.PagingOff != "" {
				if _, err := s.Run(s.Family.PagingOff); err != nil {
					return fmt.Errorf("switchcli: turning paging off: %w", err)
				}
			}
			return nil
		case 1:
			if sentPassword {
				return ErrAuth // asked for the user again: the login was refused
			}
			s.send(user)
		case 2:
			if sentPassword {
				return ErrAuth
			}
			s.send(password)
			sentPassword = true
		}
	}
	return ErrAuth
}

// confirmPrompt sends an empty line and takes the prompt only when the same one comes back: a banner line
// that looks like a prompt isn't repeated. The prompt that does come back is checked the same way, a few
// times at most.
func (s *Switch) confirmPrompt(candidate string) (string, error) {
	quiet := func(string, int) time.Duration { return loginQuiet }
	for i := 0; i < 4; i++ {
		s.send("")
		text, _, err := s.expectSettled(15*time.Second, quiet, s.Family.Prompt)
		if err != nil {
			return "", fmt.Errorf("switchcli: no prompt after login: %w", err)
		}
		got := lastLine(text)
		if got == candidate {
			return got, nil
		}
		candidate = got
	}
	return "", fmt.Errorf("switchcli: no steady prompt after login (last %q)", candidate)
}

// promptBase is what every prompt of a session starts with: the switch's name ("C1300" of "C1300#" and
// "C1300(config)#"; "(M4250-10G2F-PoE+)" of "(M4250-10G2F-PoE+) #").
func promptBase(prompt string) string {
	if strings.HasPrefix(prompt, "(") {
		if i := strings.Index(prompt, ")"); i > 0 {
			return prompt[:i+1]
		}
	}
	if i := strings.IndexAny(prompt, "(#> "); i > 0 {
		return prompt[:i]
	}
	return prompt
}

// enable reaches the privileged prompt from the ">" one.
func (s *Switch) enable(password string) (string, error) {
	s.send("enable")
	text, which, err := s.expect(15*time.Second, s.Family.Prompt, passwordRe)
	if err != nil {
		return "", fmt.Errorf("switchcli: enable: %w", err)
	}
	if which == 1 {
		s.send(password)
		if text, _, err = s.expect(15*time.Second, s.Family.Prompt); err != nil {
			return "", fmt.Errorf("switchcli: enable password: %w", err)
		}
	}
	prompt := lastLine(text)
	if !strings.HasSuffix(prompt, "#") {
		return "", fmt.Errorf("switchcli: enable did not reach the privileged prompt (%q): wrong enable password?", prompt)
	}
	return prompt, nil
}

func (s *Switch) send(line string) error {
	_, err := io.WriteString(s.in, line+s.eol)
	return err
}

var errClosed = errors.New("connection closed by the switch")

// expect reads until the output so far ends with one of res, and returns that output and which matched.
func (s *Switch) expect(timeout time.Duration, res ...*regexp.Regexp) (string, int, error) {
	return s.expectSettled(timeout, nil, res...)
}

// expectSettled is expect where a match counts only once the output has stayed quiet for as long as settle
// says (0, or a nil settle: at once). More output after a match starts the matching again, so a line that
// only looks like a prompt, followed by more, isn't taken for one. On a timeout the output is kept.
func (s *Switch) expectSettled(timeout time.Duration, settle func(text string, which int) time.Duration, res ...*regexp.Regexp) (string, int, error) {
	deadline := time.After(timeout)
	take := func(which int) (string, int, error) {
		text := s.buf
		s.buf = ""
		return text, which, nil
	}
	for {
		which := -1
		for i, re := range res {
			if re.MatchString(s.buf) {
				which = i
				break
			}
		}
		var wait <-chan time.Time
		if which >= 0 {
			d := time.Duration(0)
			if settle != nil {
				d = settle(s.buf, which)
			}
			if d <= 0 {
				return take(which)
			}
			wait = time.After(d)
		}
		select {
		case b, ok := <-s.chunks:
			if !ok {
				if which >= 0 {
					return take(which)
				}
				return s.buf, -1, errClosed
			}
			s.buf += clean(string(b))
		case <-wait:
			return take(which)
		case <-deadline:
			return s.buf, -1, fmt.Errorf("timed out after %s; last output: %q", timeout, tail(s.buf, 200))
		}
	}
}

func clean(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\x00", "")
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

func lastLine(s string) string {
	s = strings.TrimRight(s, " \n")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

// Run sends one command and returns its output (without the echoed command and the prompt after it).
// A line the switch marks as an error makes it a *CommandError. A Y/N question is answered no and is an
// error too.
func (s *Switch) Run(command string) (string, error) {
	return s.run(command)
}

// commandQuiet is how long a command's output must stay quiet after a prompt that isn't plainly the
// session's own, or after a show command's output.
const commandQuiet = 300 * time.Millisecond

func (s *Switch) run(command string) (string, error) {
	f := s.Family
	// Anything the switch printed on its own since the last command (a console message) is not this
	// command's output.
	s.drain(0)
	s.typedAt = s.current
	if err := s.send(command); err != nil {
		return "", err
	}
	var out strings.Builder
	var asked string
	answered := false
	// A show command's output can hold anything, the banner and its prompt-like lines included (show
	// running-config), so its end must also go quiet; any other command's prompt counts at once when it
	// starts with the session's name. A question waits for its answer either way.
	show := strings.HasPrefix(strings.TrimSpace(command), "show")
	settle := func(text string, which int) time.Duration {
		switch {
		case which == 2: // the pager
			return 0
		case which == 1 || show || s.base == "" || !strings.HasPrefix(lastLine(text), s.base):
			return commandQuiet
		}
		return 0
	}
	for {
		text, which, err := s.expectSettled(s.Timeout, settle, f.Prompt, f.Confirm, f.More)
		out.WriteString(text)
		if err != nil {
			return s.output(out.String(), command), fmt.Errorf("%q: %w", command, err)
		}
		switch which {
		case 1: // a question: always answered no
			answered = true
			asked = lastLine(text)
			s.send(f.No)
			continue
		case 2:
			io.WriteString(s.in, " ")
			continue
		}
		s.current = lastLine(text)
		if !strings.HasPrefix(s.current, s.base) {
			s.base = promptBase(s.current) // "hostname" renamed the switch
		}
		break
	}
	if answered {
		// After a question the prompt can come back twice, on two lines (Netgear's "write memory"): wait for
		// the switch to go quiet so the second one isn't taken as the next command's.
		s.drain(400 * time.Millisecond)
	}
	text := s.output(out.String(), command)
	if asked != "" {
		return text, &CommandError{Command: command, Lines: []string{"asked " + asked + " (answered " + f.No + ")"}, Output: text}
	}
	var bad []string
	for _, line := range strings.Split(text, "\n") {
		if f.Error.MatchString(line) {
			bad = append(bad, strings.TrimSpace(line))
		}
	}
	if len(bad) > 0 {
		return text, &CommandError{Command: command, Lines: bad, Output: text}
	}
	return text, nil
}

// drain throws away what the switch has sent, waiting until it has been quiet for quiet (0: what is
// already here).
func (s *Switch) drain(quiet time.Duration) {
	defer func() { s.buf = "" }()
	for {
		if quiet == 0 {
			select {
			case _, ok := <-s.chunks:
				if !ok {
					return
				}
			default:
				return
			}
			continue
		}
		select {
		case _, ok := <-s.chunks:
			if !ok {
				return
			}
		case <-time.After(quiet):
			return
		}
	}
}

var netgearPager = regexp.MustCompile(`--More-- or \(q\)uit *\n?`)

// output drops the echoed command (first line), the prompt (last line) and pager lines.
func (s *Switch) output(text, command string) string {
	text = netgearPager.ReplaceAllString(text, "")
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && strings.Contains(lines[0], strings.TrimSpace(command)) {
		lines = lines[1:]
	}
	if len(lines) > 0 && s.Family.Prompt.MatchString("\n"+lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	kept := lines[:0]
	for _, l := range lines {
		if !strings.Contains(l, "More: <space>") {
			kept = append(kept, l)
		}
	}
	return strings.TrimRight(strings.TrimLeft(strings.Join(kept, "\n"), "\n"), "\n ") // keeps the first line's indent
}

// Close logs out.
func (s *Switch) Close() error {
	if s.in != nil {
		s.send("exit")
		if s.Family.Enable {
			s.send("logout") // Netgear's privileged "exit" drops to ">", which "logout" ends
		}
	}
	return s.close()
}

// Telnet option negotiation (RFC 854/857/858): the switch may echo and suppress go-ahead; every other
// option is refused.
func negotiate(conn io.Writer, b []byte) []byte {
	const (
		iac, dont, do, wont, will, sb, se = 255, 254, 253, 252, 251, 250, 240
		optEcho, optSGA                   = 1, 3
	)
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
				conn.Write([]byte{iac, reply, opt})
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
