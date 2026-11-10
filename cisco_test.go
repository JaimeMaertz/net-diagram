package netdiagram

import (
	"errors"
	"strings"
	"testing"
)

func TestCiscoAllowlist(t *testing.T) {
	for _, c := range append(append([]string{}, CiscoCommands...), "show lldp neighbors gi1/0/5", "show lldp neighbors te1/0/1") {
		if !Allowed(c) {
			t.Errorf("%q should be allowed", c)
		}
	}
	for _, c := range []string{"show running-config", "write memory", "configure terminal", "show lldp neighbors gi1/0/5 x"} {
		if Allowed(c) {
			t.Errorf("%q must not be allowed", c)
		}
	}
}

func TestSaveLoadOutputsCiscoPort(t *testing.T) {
	dir := t.TempDir()
	in := map[string]string{"show lldp neighbors gi1/0/5": "x", "show port description 0/12": "y", "show system": "z"}
	if err := SaveOutputs(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadOutputs(dir)
	if err != nil {
		t.Fatal(err)
	}
	for c, v := range in {
		if out[c] != v {
			t.Errorf("%q did not round-trip: got %q", c, out[c])
		}
	}
	if VendorOf(out) != "Cisco" {
		t.Errorf("VendorOf: %q", VendorOf(out))
	}
}

func TestCollectCisco(t *testing.T) {
	sent := []string{}
	run := func(c string) (string, error) {
		sent = append(sent, c)
		switch c {
		case "show lldp neighbors":
			return "Port  Device ID  Port ID\n----  ---------  -------\ngi1/0/5  aa:bb  gi1/0/1\ngi1/0/9  cc:dd  eth0\n", nil
		case "show power inline":
			return "", errors.New("% Unrecognized command") // a model without PoE
		}
		return "ok", nil
	}
	out, err := CollectCisco(run)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(sent, "|")
	for _, want := range []string{"show lldp neighbors gi1/0/5", "show lldp neighbors gi1/0/9"} {
		if !strings.Contains(joined, want) {
			t.Errorf("per-port command %q not sent: %v", want, sent)
		}
	}
	if !strings.HasPrefix(out["show power inline"], "% netdiagram:") {
		t.Errorf("a refused command should be kept as its error, got %q", out["show power inline"])
	}
	if _, err := Parse("Cisco", "1.2.3.4", out); err == nil {
		t.Error("Parse should refuse output with no interfaces table")
	}
}

// The fixtures are the CLI guide's examples (testdata/c1300-guide/README.md), not a real switch.
func loadC1300Guide(t *testing.T) Switch {
	t.Helper()
	out, err := LoadOutputs("testdata/c1300-guide")
	if err != nil {
		t.Fatal(err)
	}
	if VendorOf(out) != "Cisco" {
		t.Fatalf("VendorOf: %q", VendorOf(out))
	}
	sw, err := Parse("Cisco", "10.0.0.2", out)
	if err != nil {
		t.Fatal(err)
	}
	return sw
}

func TestParseCiscoIdentity(t *testing.T) {
	sw := loadC1300Guide(t)
	if sw.Name != "switch151400" || sw.Model != "xx350-4x-K9" || sw.Serial != "123456789" || sw.Firmware != "12.03" {
		t.Errorf("identity: name %q model %q serial %q firmware %q", sw.Name, sw.Model, sw.Serial, sw.Firmware)
	}
	for _, m := range []string{"00:24:AB:15:14:00", "00:00:26:08:13:23"} { // system MAC, and the "self" entry
		if !contains(sw.MACs, m) {
			t.Errorf("switch MAC %s missing: %v", m, sw.MACs)
		}
	}
}

func TestParseCiscoPorts(t *testing.T) {
	sw := loadC1300Guide(t)
	p := map[string]Port{}
	var names []string
	for _, x := range sw.Ports {
		p[x.Name] = x
		names = append(names, x.Name)
	}
	if strings.Join(names, ",") != "gi1/0/1,gi1/0/2,te1/0/1" {
		t.Fatalf("ports %v: want the three physical ports, no port-channel", names)
	}
	if !p["gi1/0/1"].Up || p["gi1/0/2"].Up || p["gi1/0/1"].Speed != "1000 Full" {
		t.Errorf("link: %+v / %+v", p["gi1/0/1"], p["gi1/0/2"])
	}
	if p["gi1/0/1"].Description != "Port that should be used for management only" {
		t.Errorf("description: %q", p["gi1/0/1"].Description)
	}
	if p["gi1/0/1"].PoEmW != 15400 || p["gi1/0/1"].PoEStatus != "On" || p["gi1/0/2"].PoEStatus != "Searching" {
		t.Errorf("PoE: %d mW %q / %q (the unit table above it must not be read as ports)", p["gi1/0/1"].PoEmW, p["gi1/0/1"].PoEStatus, p["gi1/0/2"].PoEStatus)
	}
	if len(p["gi1/0/1"].MACs) != 1 || p["gi1/0/1"].MACs[0].MAC != "00:3F:BD:45:5A:B1" {
		t.Errorf("gi1/0/1 MACs: %+v", p["gi1/0/1"].MACs)
	}
	n := p["gi1/0/1"].Neighbors
	if len(n) != 1 || n[0].SystemName != "ts-7800-2" || n[0].MgmtIP != "172.16.1.1" || n[0].CapsEnabled != "bridge" {
		t.Errorf("gi1/0/1 neighbor: %+v", n)
	}
	var arp []string
	for _, a := range sw.ARP {
		arp = append(arp, a.IP)
	}
	if strings.Join(arp, ",") != "10.7.1.102,10.7.1.135,11.7.1.135,12.10.1.13" {
		t.Errorf("ARP (VLAN cells hold a space, and one is empty): %v", arp)
	}
}

func TestCiscoCaps(t *testing.T) {
	if got := ciscoCaps("B,R"); got != "bridge, router" || !isBridge(Neighbor{CapsEnabled: got}) {
		t.Errorf("ciscoCaps(B,R) = %q", got)
	}
	if isBridge(Neighbor{CapsEnabled: ciscoCaps("H")}) {
		t.Error("a host is not a switch")
	}
}

// A Netgear and a Cisco cabled together: the link is found across vendors.
func TestCiscoNetgearLink(t *testing.T) {
	ng := loadM4250(t)
	cs := loadC1300Guide(t)
	// Netgear's 0/10 sees the Cisco by its system MAC; the Cisco's gi1/0/1 sees the Netgear.
	for i := range ng.Ports {
		if ng.Ports[i].Name == "0/10" {
			ng.Ports[i].Neighbors = append(ng.Ports[i].Neighbors, Neighbor{ChassisID: "00:24:AB:15:14:00", CapsEnabled: "bridge"})
		}
	}
	for i := range cs.Ports {
		if cs.Ports[i].Name == "gi1/0/1" {
			cs.Ports[i].Neighbors = []Neighbor{{ChassisID: "28:94:01:AF:35:BA", CapsEnabled: "bridge"}}
		}
	}
	snap := Build([]Switch{ng, cs}, DeviceDB{}, OUI{}, nil, nil)
	found := false
	for _, l := range snap.Links {
		if l.ToSwitch != "" && ((l.FromPort == "0/10" && l.ToPort == "gi1/0/1") || (l.FromPort == "gi1/0/1" && l.ToPort == "0/10")) {
			found = true
		}
	}
	if !found {
		t.Errorf("no Netgear 0/10 ↔ Cisco gi1/0/1 link: %+v", snap.Links)
	}
}
