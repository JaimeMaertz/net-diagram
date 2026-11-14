package netdiagram

import (
	"html"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fixtures are three TP-Link JetStream switches in a chain, read over the CLI with LLDP on:
// testdata/tl-sg3428xmp (a TL-SG3428XMP 3.0, firmware 3.0.3, over SSH), testdata/tl-sg3428x-b and
// testdata/tl-sg3428x (TL-SG3428X 1.0, firmware 1.0.7, over telnet, no PoE). XMP Gi1/0/5 → X-b Gi1/0/1,
// X-b Te1/0/25 → X Te1/0/25; a TesiraForte is on the X's Gi1/0/1. Both X keep their model as their name.
func loadTPLink(t *testing.T, dir, host string) Switch {
	t.Helper()
	out, err := LoadOutputs("testdata/" + dir)
	if err != nil {
		t.Fatal(err)
	}
	if v := VendorOf(out); v != "TP-Link" {
		t.Fatalf("VendorOf = %q, want TP-Link", v)
	}
	sw, err := ParseTPLink(host, out)
	if err != nil {
		t.Fatal(err)
	}
	return sw
}

func portMap(sw Switch) map[string]Port {
	p := map[string]Port{}
	for _, x := range sw.Ports {
		p[x.Name] = x
	}
	return p
}

func TestParseTPLinkIdentity(t *testing.T) {
	for _, c := range []struct{ dir, model, firmware, mac string }{
		{"tl-sg3428xmp", "TL-SG3428XMP", "3.0.3 Build 20230602", "5C:E9:31:1B:88:27"},
		{"tl-sg3428x", "TL-SG3428X", "1.0.7 Build 20220606", "40:ED:00:E3:A9:C4"},
	} {
		sw := loadTPLink(t, c.dir, "10.0.0.1")
		if sw.Model != c.model || sw.Firmware != c.firmware || sw.Serial == "" || sw.Name != c.model {
			t.Errorf("%s identity: model %q firmware %q serial %q name %q", c.dir, sw.Model, sw.Firmware, sw.Serial, sw.Name)
		}
		if len(sw.MACs) != 1 || sw.MACs[0] != c.mac {
			t.Errorf("%s own MACs = %v, want [%s]", c.dir, sw.MACs, c.mac)
		}
	}
}

func TestParseTPLinkPorts(t *testing.T) {
	sw := loadTPLink(t, "tl-sg3428xmp", "10.0.0.1")
	if len(sw.Ports) != 28 {
		t.Fatalf("got %d ports, want 28 (24 Gi + 4 Te)", len(sw.Ports))
	}
	p := portMap(sw)
	if g := p["Gi1/0/1"]; !g.Up || g.Speed != "100 Full" {
		t.Errorf("Gi1/0/1 = up %v speed %q, want up at 100 Full", g.Up, g.Speed)
	}
	if g := p["Gi1/0/2"]; g.Up || g.Speed != "" {
		t.Errorf("Gi1/0/2 should be down with no speed: %+v", g)
	}
	if te := p["Te1/0/25"]; te.Name == "" || te.Up {
		t.Errorf("Te1/0/25 missing or up: %+v", te)
	}
	// PoE as the fixture holds it: Gi1/0/9 draws 4.3 W, Gi1/0/13 8.0 W; an unpowered port stays 0 and OFF.
	if p["Gi1/0/9"].PoEmW != 4300 || p["Gi1/0/9"].PoEStatus != "ON" || p["Gi1/0/13"].PoEmW != 8000 {
		t.Errorf("PoE: Gi1/0/9 %d %q, Gi1/0/13 %d", p["Gi1/0/9"].PoEmW, p["Gi1/0/9"].PoEStatus, p["Gi1/0/13"].PoEmW)
	}
	if p["Gi1/0/1"].PoEmW != 0 || p["Gi1/0/1"].PoEStatus != "OFF" {
		t.Errorf("Gi1/0/1 PoE: %d %q", p["Gi1/0/1"].PoEmW, p["Gi1/0/1"].PoEStatus)
	}
	// The MAC table (under its "MAC Address Table" title): 36 entries, the other two switches on Gi1/0/5.
	n := 0
	for _, x := range sw.Ports {
		n += len(x.MACs)
	}
	if n != 36 {
		t.Errorf("learned MACs = %d, want 36", n)
	}
	var on5 []string
	for _, m := range p["Gi1/0/5"].MACs {
		on5 = append(on5, m.MAC)
	}
	for _, m := range []string{"40:ED:00:E3:A9:C4", "40:ED:00:E3:AB:60"} {
		if !contains(on5, m) {
			t.Errorf("Gi1/0/5 should have learned %s: %v", m, on5)
		}
	}
	if len(sw.ARP) != 2 {
		t.Errorf("ARP = %v, want 2 entries", sw.ARP)
	}
}

func TestParseTPLinkNoPoE(t *testing.T) {
	sw := loadTPLink(t, "tl-sg3428x", "10.0.0.2")
	for _, x := range sw.Ports {
		if x.PoEmW != 0 || x.PoEStatus != "" {
			t.Errorf("%s has PoE on a switch without it: %d %q", x.Name, x.PoEmW, x.PoEStatus)
		}
	}
	if te := portMap(sw)["Te1/0/25"]; !te.Up || te.Speed != "10G Full" || len(te.MACs) < 10 {
		t.Errorf("Te1/0/25 (the uplink): %+v", te)
	}
}

func TestBuildLinksTwoTPLinks(t *testing.T) {
	xmp := loadTPLink(t, "tl-sg3428xmp", "10.0.0.1")
	x := loadTPLink(t, "tl-sg3428x", "10.0.0.2")
	snap := Build([]Switch{xmp, x}, DeviceDB{}, OUI{}, nil, nil)
	found := false
	for _, l := range snap.Links {
		if l.FromSwitch == "10.0.0.1" && l.FromPort == "Gi1/0/5" && l.ToSwitch == "10.0.0.2" ||
			l.FromSwitch == "10.0.0.2" && l.FromPort == "Te1/0/25" && l.ToSwitch == "10.0.0.1" {
			found = true
		}
	}
	if !found {
		t.Errorf("no link between the XMP's Gi1/0/5 and the X's Te1/0/25 found from their MAC tables: %+v", snap.Links)
	}
	for _, d := range snap.Devices {
		for _, i := range d.Interfaces {
			if i.MAC == "40:ED:00:E3:A9:C4" || i.MAC == "5C:E9:31:1B:88:27" {
				t.Errorf("a read switch's own MAC was drawn as a device: %+v", d)
			}
		}
	}
}

func TestCollectTPLink(t *testing.T) {
	out, err := LoadOutputs("testdata/tl-sg3428x")
	if err != nil {
		t.Fatal(err)
	}
	lldpOn := out["show lldp"]
	if !strings.Contains(lldpOn, "LLDP Status:                  Enabled") {
		t.Fatalf("fixture's show lldp isn't the LLDP-on one:\n%s", lldpOn)
	}
	var sent []string
	got, err := CollectTPLink(func(c string) (string, error) {
		if !Allowed(c) {
			t.Errorf("collector asked for %q, which the allowlist refuses", c)
		}
		sent = append(sent, c)
		switch c {
		case "show power inline information interface":
			return "----------------^\nError: Bad command\n", nil
		case "show lldp":
			return lldpOn, nil
		}
		if strings.HasPrefix(c, "show lldp neighbor-information interface") {
			return "neighbor text", nil
		}
		return out[c], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["show power inline information interface"] != "% netdiagram: Bad command" {
		t.Errorf("refused command kept as %q", got["show power inline information interface"])
	}
	// LLDP on: one neighbor read per port that is up (Gi1/0/1, 2, 3, 5 and Te1/0/25).
	var lldp []string
	for _, c := range sent {
		if strings.HasPrefix(c, "show lldp neighbor-information interface") {
			lldp = append(lldp, c)
		}
	}
	want := []string{
		"show lldp neighbor-information interface gigabitEthernet 1/0/1",
		"show lldp neighbor-information interface gigabitEthernet 1/0/2",
		"show lldp neighbor-information interface gigabitEthernet 1/0/3",
		"show lldp neighbor-information interface gigabitEthernet 1/0/5",
		"show lldp neighbor-information interface ten-gigabitEthernet 1/0/25",
	}
	if strings.Join(lldp, "|") != strings.Join(want, "|") {
		t.Errorf("LLDP reads:\n got %q\nwant %q", lldp, want)
	}
	// LLDP off: no neighbor reads.
	out["show lldp"] = strings.Replace(lldpOn, "Enabled", "Disabled", 1)
	if cmds := TPLinkLLDPCommands(out); len(cmds) != 0 {
		t.Errorf("LLDP is off but these were asked for: %v", cmds)
	}
}

// fakeTerm plays a switch CLI: it answers each line written with a scripted reply.
type fakeTerm struct {
	replies  map[string]string
	pending  []byte
	deadline time.Time
	wrote    []string
	line     []byte
}

func (f *fakeTerm) Read(p []byte) (int, error) {
	if len(f.pending) == 0 {
		return 0, os.ErrDeadlineExceeded
	}
	n := copy(p, f.pending)
	f.pending = f.pending[n:]
	return n, nil
}

func (f *fakeTerm) Write(p []byte) (int, error) {
	for _, b := range p {
		switch b {
		case '\r':
			cmd := string(f.line)
			f.wrote = append(f.wrote, cmd)
			f.line = nil
			f.pending = append(f.pending, []byte(cmd+"\r\n"+f.replies[cmd])...)
		case '?':
			f.pending = append(f.pending, []byte(f.replies[string(f.line)+"?"])...)
		case ' ':
			if len(f.line) == 0 && len(f.pending) == 0 {
				f.pending = append(f.pending, []byte(f.replies["<space>"])...) // a pager answered
				continue
			}
			f.line = append(f.line, b)
		default:
			f.line = append(f.line, b)
		}
	}
	return len(p), nil
}

func (f *fakeTerm) Close() error                       { return nil }
func (f *fakeTerm) SetReadDeadline(t time.Time) error  { f.deadline = t; return nil }
func (f *fakeTerm) SetWriteDeadline(t time.Time) error { return nil }

var _ io.ReadWriter = (*fakeTerm)(nil)

func TestCLISessionTPLinkPagerAndHelp(t *testing.T) {
	const prompt = "TL-SG3428XMP#"
	f := &fakeTerm{replies: map[string]string{
		// The first page, then TP-Link's pager, erased with NUL, CR, spaces, CR when a key is pressed.
		"show arp": "Interface  Address\r\nVLAN1  192.0.2.1\r\nPress any key to continue (Q to quit)",
		"<space>":  "\x00\r                                      \rVLAN1  192.0.2.2\r\n" + prompt,
		"show ?":   "\r\n arp   - Display ARP information\r\n <cr>\r\n\r\n" + prompt + "show ",
		"show ":    "Error: Incomplete command\r\n\r\n" + prompt,
	}}
	s := &CLISession{conn: f, eol: "\r", Prompt: prompt}
	out, err := s.Run("show arp")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Press any key") || strings.ContainsAny(out, "\x00\r") || !strings.Contains(out, "\nVLAN1  192.0.2.2") {
		t.Errorf("paged output not cleaned up:\n%q", out)
	}
	help, err := s.Run("show ?")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(help, "arp   - Display ARP information") {
		t.Errorf("help listing lost: %q", help)
	}
	if got := f.wrote[len(f.wrote)-1]; got != "show " {
		t.Errorf("after the help, Enter should run the retyped prefix; the switch got %q", got)
	}
	if _, err := s.Run("configure"); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("a command off the allowlist was not refused: %v", err)
	}
}

// Three switches in a chain, read from the real fixtures: the XMP's Gi1/0/5 goes to the second X's
// Gi1/0/1, whose Te1/0/25 goes to the first X's Te1/0/25. Each end switch learns both others on its one
// uplink, so the MAC tables alone would also "link" the two ends; the middle switch learning them on
// different ports is what rules that out.
func TestBuildLinksChainOfThree(t *testing.T) {
	xmp := loadTPLink(t, "tl-sg3428xmp", "10.0.0.1")
	x := loadTPLink(t, "tl-sg3428x", "10.0.0.2")
	mid := loadTPLink(t, "tl-sg3428x-b", "10.0.0.3")
	snap := Build([]Switch{xmp, x, mid}, DeviceDB{}, OUI{}, nil, nil)
	got := map[string]bool{}
	for _, l := range snap.Links {
		a, b := l.FromSwitch+" "+l.FromPort, l.ToSwitch+" "+l.ToPort
		if a > b {
			a, b = b, a
		}
		got[a+" — "+b] = true
	}
	want := []string{"10.0.0.1 Gi1/0/5 — 10.0.0.3 Gi1/0/1", "10.0.0.2 Te1/0/25 — 10.0.0.3 Te1/0/25"}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing link %s", w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("links = %v, want only %v", got, want)
	}
	// Every link label stays on the sheet, including the one whose lower port is the leftmost.
	for _, m := range regexp.MustCompile(`<text x="(-?\d+)" y="\d+" class="small" text-anchor="(start|end)">([^<]*↔[^<]*)</text>`).FindAllStringSubmatch(SVG(snap), -1) {
		x, _ := strconv.ParseFloat(m[1], 64)
		if m[2] == "end" && x-labelWidth(html.UnescapeString(m[3])) < 0 {
			t.Errorf("link label %q runs off the left edge (ends at x=%v)", m[3], x)
		}
	}
}

// A port on a shared segment (an unmanaged switch with two read switches on it): both are direct.
func TestDirectMACNeighborsSharedSegment(t *testing.T) {
	sw := func(host, mac string, ports map[string][]string) Switch {
		s := Switch{Host: host, MACs: []string{mac}}
		for name, macs := range ports {
			p := Port{Name: name}
			for _, m := range macs {
				p.MACs = append(p.MACs, MACEntry{MAC: m})
			}
			s.Ports = append(s.Ports, p)
		}
		return s
	}
	const a, b, c = "02:00:00:00:00:0A", "02:00:00:00:00:0B", "02:00:00:00:00:0C"
	switches := []Switch{
		sw("A", a, map[string][]string{"1": {b, c}}),
		sw("B", b, map[string][]string{"9": {a, c}}),
		sw("C", c, map[string][]string{"9": {a, b}}),
	}
	if d := directMACNeighbors("A", []string{"B", "C"}, switches); len(d) != 2 {
		t.Errorf("shared segment: direct = %v, want both B and C", d)
	}
	// Now B sits between A and C: B learns A on port 1 and C on port 2.
	switches[1] = sw("B", b, map[string][]string{"1": {a}, "2": {c}})
	if d := directMACNeighbors("A", []string{"B", "C"}, switches); len(d) != 1 || d[0] != "B" {
		t.Errorf("chain: direct = %v, want only B", d)
	}
}

func TestParseTPLinkLLDP(t *testing.T) {
	xmp := portMap(loadTPLink(t, "tl-sg3428xmp", "10.0.0.1"))
	n := xmp["Gi1/0/5"].Neighbors
	if len(n) != 1 {
		t.Fatalf("XMP Gi1/0/5 neighbors = %+v, want one", n)
	}
	want := Neighbor{ChassisID: "40:ED:00:E3:AB:60", PortID: "gigabitEthernet 1/0/1", PortDesc: "gigabitEthernet 1/0/1",
		SystemName: "TL-SG3428X", SystemDesc: "JetStream 24-Port Gigabit L2+ Managed Switch with 4 10GE SFP+ Slots",
		MgmtIP: "192.168.1.40", CapsEnabled: "bridge, router"}
	if n[0] != want {
		t.Errorf("XMP Gi1/0/5 neighbor:\n got %+v\nwant %+v", n[0], want)
	}
	if len(xmp["Gi1/0/9"].Neighbors) != 0 {
		t.Errorf(`"No Neighbor." read as neighbors: %+v`, xmp["Gi1/0/9"].Neighbors)
	}
	x := portMap(loadTPLink(t, "tl-sg3428x", "10.0.0.2"))
	if n := x["Gi1/0/1"].Neighbors; len(n) != 1 || n[0].SystemName != "TesiraForte01234567" || n[0].CapsEnabled != "station only" || n[0].MgmtIP != "192.168.1.9" {
		t.Errorf("X Gi1/0/1 (the Tesira): %+v", n)
	}
}

// Two read switches share a system name (both kept their model as name): an LLDP neighbor known only by
// that name must not be taken for either of them.
func TestBuildSharedSwitchName(t *testing.T) {
	a := Switch{Host: "A", Name: "CORE", MACs: []string{"02:00:00:00:00:0A"}, Ports: []Port{{Name: "1", Up: true,
		Neighbors: []Neighbor{{ChassisID: "02:00:00:00:00:99", SystemName: "SAME", CapsEnabled: "bridge"}}}}}
	b := Switch{Host: "B", Name: "SAME", MACs: []string{"02:00:00:00:00:0B"}, Ports: []Port{{Name: "1"}}}
	c := Switch{Host: "C", Name: "SAME", MACs: []string{"02:00:00:00:00:0C"}, Ports: []Port{{Name: "1"}}}
	snap := Build([]Switch{a, b, c}, DeviceDB{}, OUI{}, nil, nil)
	if len(snap.Links) != 1 || snap.Links[0].ToSwitch != "" || snap.Links[0].ToName != "SAME" {
		t.Errorf("links = %+v, want one to an unread switch named SAME", snap.Links)
	}
}

func TestTPLinkCaps(t *testing.T) {
	for in, want := range map[string]string{
		"Bridge Router ":                   "bridge, router",
		"Station Only ":                    "station only",
		"Bridge WLAN Access Point Router ": "bridge, wlan access point, router",
		"":                                 "",
	} {
		if got := tplinkCaps(in); got != want {
			t.Errorf("tplinkCaps(%q) = %q, want %q", in, got, want)
		}
	}
}
