package switchcli

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPrompt(t *testing.T) {
	for _, s := range []string{"\nC1300#", "\nC1300# ", "\nC1300(config)#", "\nSW-01(config-if)#", "C1300>",
		"\nN\nSW-01(config)#SW-01(config)#"} {
		if !Cisco.Prompt.MatchString(s) {
			t.Errorf("%q should be a prompt", s)
		}
	}
	for _, s := range []string{"\n description lab #", "\nvlan 10 name AV", "\nC1300#show vlan"} {
		if Cisco.Prompt.MatchString(s) {
			t.Errorf("%q should not be a prompt", s)
		}
	}
	for _, s := range []string{"\n(M4250-10G2F-PoE+) >", "\n(M4250-10G2F-PoE+) #", "\n(M4250-10G2F-PoE+)(Config)#"} {
		if !Netgear.Prompt.MatchString(s) {
			t.Errorf("%q should be a NETGEAR prompt", s)
		}
	}
	for _, q := range []string{"Overwrite file [startup-config].... (Y/N)[N] ?", "Overwrite file [startup-config] ?[Yes/press any key for no]....",
		"Queue number 8 is reserved for special traffic\nAre you sure [y/n]?  (Y/N)[Y] "} {
		if !Cisco.Confirm.MatchString(q) {
			t.Errorf("question not matched: %q", q)
		}
	}
}

func TestErrorLines(t *testing.T) {
	for line, want := range map[string]bool{
		"% Unrecognized command":                                      true,
		"   % bad parameter value":                                    true,
		"%AAA-E-SOMETHING: failed":                                    true,
		"%COPY-I-FILECPY: Files Copy - source URL":                    false,
		"%COPY-N-TRAP: The copy operation was completed successfully": false,
		" description 100%":                                           false,
	} {
		if got := Cisco.Error.MatchString(line); got != want {
			t.Errorf("%q: error = %v, want %v", line, got, want)
		}
	}
}

func TestOutput(t *testing.T) {
	text := "show vlan\nVlan  Name\n----  ----\n1     1\nMore: <space>,  Quit: q\n10    AV\n\nC1300#"
	got := (&Switch{Family: Cisco}).output(text, "show vlan")
	want := "Vlan  Name\n----  ----\n1     1\n10    AV"
	if got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if got := clean("ab\x08c\r\n\x1b[2Kd"); got != "ac\nd" {
		t.Errorf("clean = %q", got)
	}
}

// fakeSG250 answers like a Cisco SG250 as its CLI guide (Sx250 2.4.5) describes it, logged in over telnet
// with password authentication off, so the switch asks "User Name:" and "Password:" itself, behind a banner
// built to look like prompts. Each piece is its own write with a pause, so the client sees a prompt-like line
// at the very end of what it has read:
//
//   - the login banner holds "AVSW1#" and a line ending "login:", then waits for Enter (a long banner does:
//     "After 510 characters, press <Enter> to continue", admin guide p. 48);
//   - the welcome banner after login holds "=====>" and the real prompt's text;
//   - show running-config holds the banner again;
//   - "write memory" asks its question, which a read-only session must answer no.
type fakeSG250 struct {
	mu         sync.Mutex
	received   []string
	intoBanner bool // a line arrived while the login banner was waiting for Enter, and it wasn't Enter
}

const sg250Banner = "**** AV network: authorised use only ****\r\nAVSW1#"

func (f *fakeSG250) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(parts ...string) {
		for _, p := range parts {
			c.Write([]byte(p))
			time.Sleep(30 * time.Millisecond) // each piece its own read on the other side
		}
	}
	line := func() string {
		l, err := r.ReadString('\n')
		if err != nil {
			return "\x00eof"
		}
		l = strings.TrimRight(l, "\r\n")
		f.mu.Lock()
		f.received = append(f.received, l)
		f.mu.Unlock()
		return l
	}
	host := "av-sw1"
	say("\r\n"+sg250Banner, "\r\nAuthorized users only. Please login:", "\r\n-- press <Enter> to continue --")
	if l := line(); l != "" {
		f.mu.Lock()
		f.intoBanner = true
		f.mu.Unlock()
		return
	}
	say("\r\n\r\nUser Name:")
	if line() != "admin" {
		return
	}
	say("Password:")
	if line() != "secret" {
		say("\r\n\r\nUser Name:")
		line()
		return
	}
	say("\r\nWelcome!\r\n=====>", "\r\n"+host+"#", "\r\n\r\n"+host+"#")
	for {
		cmd := line()
		if cmd == "\x00eof" || cmd == "exit" {
			return
		}
		say(cmd + "\r\n")
		switch cmd {
		case "", "terminal datadump":
		case "show running-config":
			say("config-file-header\r\n"+host+"\r\nbanner login ^C\r\n"+sg250Banner,
				"\r\n"+host+"#",
				"\r\n^C\r\nip igmp snooping\r\n!\r\nend\r\n")
		case "write memory":
			say("Overwrite file [startup-config] ?[Yes/press any key for no]....")
			if line() == "Y" {
				say("Copy succeeded\r\n")
			}
		default:
			say("% Unrecognized command\r\n")
		}
		say("\r\n" + host + "#")
	}
}

func startFakeSG250(t *testing.T) (*fakeSG250, string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeSG250{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f, ln.Addr().String()
}

// A banner that looks like prompts and questions, at login and in the running configuration, doesn't derail
// the session; a command that asks a question is answered no and is an error; nothing but the login, the
// paging line, the commands and that no is ever sent.
func TestSG250Banner(t *testing.T) {
	old := loginStall
	loginStall = 600 * time.Millisecond
	t.Cleanup(func() { loginStall = old })

	f, addr := startFakeSG250(t)
	s, err := Open(Options{Host: addr, User: "admin", Password: "secret", Family: Cisco, Transport: "telnet"})
	if err != nil {
		f.mu.Lock()
		t.Fatalf("login: %v (sent %q, typed into the banner: %v)", err, f.received, f.intoBanner)
	}
	if s.Prompt != "av-sw1#" {
		t.Errorf("prompt %q", s.Prompt)
	}
	cfg, err := s.Run("show running-config")
	if err != nil || !strings.Contains(cfg, "AVSW1#") || !strings.HasSuffix(strings.TrimSpace(cfg), "end") {
		t.Errorf("running config cut short at a banner line: %q, %v", cfg, err)
	}
	if _, err := s.Run("write memory"); err == nil {
		t.Error("a question answered no should be an error")
	}
	if out, err := s.Run("show running-config"); err != nil || !strings.HasPrefix(out, "config-file-header") {
		t.Errorf("after the question, next command got %q, %v", out, err)
	}
	s.Close()
	time.Sleep(100 * time.Millisecond)

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.intoBanner {
		t.Error("typed into the login banner")
	}
	got := strings.Join(f.received, "|")
	if want := "|admin|secret|"; !strings.Contains("|"+got, want) {
		t.Errorf("sent %q, missing %q", got, want)
	}
	for _, l := range f.received {
		if l == "Y" || l == "y" {
			t.Errorf("answered yes: sent %q", got)
		}
	}
}
