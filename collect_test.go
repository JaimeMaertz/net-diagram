package netdiagram

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// hostileBanner is a login banner whose reads each end on something that looks like a prompt or a login
// question (the shape that breaks a session taking the first prompt-like text it reads).
var hostileBanner = []string{
	"\r\n=====================================\r\n AV-SW1 (M4250-10G2F-PoE+) #",
	"\r\n Authorized users only. Site login:",
	"\r\n=====>",
}

// fakeM4250 answers telnet as an M4250 does, after hostileBanner, with each show command's
// recorded output from dir; anything else is refused as the switch refuses it.
type fakeM4250 struct {
	outputs  map[string]string
	mu       sync.Mutex
	received []string
}

func (f *fakeM4250) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { c.Write([]byte(s)) }
	line := func() string {
		l, err := r.ReadString('\n')
		if err != nil {
			return "\x00eof"
		}
		l = strings.TrimRight(l, "\r\n")
		for len(l) >= 3 && l[0] == 255 {
			l = l[3:] // the client's telnet option replies
		}
		f.mu.Lock()
		f.received = append(f.received, l)
		f.mu.Unlock()
		return l
	}
	c.Write([]byte{255, 251, 1, 255, 251, 3}) // WILL ECHO, WILL SGA
	for _, b := range hostileBanner {
		say(b)
		time.Sleep(60 * time.Millisecond) // each its own network read
	}
	say("\r\n\r\nUser:")
	// The login prompt discards what was typed before it asked (into the banner), as a login program does.
	c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	for {
		if _, err := r.ReadByte(); err != nil {
			break
		}
	}
	c.SetReadDeadline(time.Time{})
	r = bufio.NewReader(c)
	if line() != "admin" {
		return
	}
	say("Password:")
	if line() != "secret" {
		say("\r\nUser:")
		line()
		return
	}
	say("******\r\n\r\n(M4250-10G2F-PoE+) >")
	mode := ">"
	for {
		cmd := line()
		if cmd == "\x00eof" || cmd == "logout" {
			return
		}
		say(cmd + "\r\n")
		switch {
		case cmd == "":
		case cmd == "enable" && mode == ">":
			mode = "#"
		case cmd == "terminal length 0" && mode == "#":
		case mode == "#" && f.outputs[cmd] != "":
			say(strings.ReplaceAll(strings.TrimRight(f.outputs[cmd], "\r\n"), "\n", "\r\n") + "\r\n")
		default:
			say("                   ^\r\n% Invalid input detected at '^' marker.\r\n")
		}
		say("\r\n(M4250-10G2F-PoE+) " + mode)
	}
}

func startM4250(t *testing.T) (*fakeM4250, string) {
	t.Helper()
	out, err := LoadOutputs("testdata/m4250")
	if err != nil {
		t.Fatal(err)
	}
	delete(out, "show poe port info all") // one show this firmware "lacks": refused, which must not end the read
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeM4250{outputs: out}
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

// A Netgear read goes through net-switch-config's session: the banner's prompt-like lines don't derail
// the login, every command sent is a read, a refused show doesn't end the read, and the result parses as
// the recorded outputs do.
func TestCollectNetgearHostileBanner(t *testing.T) {
	f, addr := startM4250(t)
	out, err := Collect("Netgear", addr, "admin", "secret")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse("Netgear", addr, out)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := LoadOutputs("testdata/m4250")
	want := ParseNetgear(addr, saved)
	if len(got.Ports) != len(want.Ports) || len(got.Ports) == 0 || got.Model != want.Model {
		t.Errorf("parsed %d ports, model %q; want %d, %q", len(got.Ports), got.Model, len(want.Ports), want.Model)
	}
	if !strings.Contains(out["show poe port info all"], "Invalid input") {
		t.Errorf("refused show: %q", out["show poe port info all"])
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Join(f.received[:5], "|") != "admin|secret||enable|terminal length 0" {
		t.Errorf("login sent %q", f.received[:5])
	}
	for _, c := range f.received[5:] {
		if c != "" && c != "logout" && c != "exit" && !Allowed(c) { // exit and logout: the session's close
			t.Errorf("sent %q, not on the allowlist", c)
		}
	}
}

// The fake bites: net-diagram's own telnet login, which takes the first prompt-like text, is derailed by
// the banner (it types the user name into it).
func TestHostileBannerBreaksOldTelnet(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the old login's 10 s password timeout")
	}
	f, addr := startM4250(t)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	s, err := startCLI(conn, "\r\n", userPrompt, "admin", "secret", "")
	if err == nil {
		s.Close()
	}
	time.Sleep(200 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil && len(f.received) >= 2 && f.received[0] == "admin" && f.received[1] == "secret" {
		t.Fatalf("the old login got through the banner (%q); the fake doesn't test what it should", f.received)
	}
}
