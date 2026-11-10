package netdiagram

import (
	"strings"
	"testing"
)

// The fixtures are a real M4250-10G2F-PoE+ (172.22.0.4, firmware 13.0.6.8), with MACs and serials altered.
func loadM4250(t *testing.T) Switch {
	t.Helper()
	out, err := LoadOutputs("testdata/m4250")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["show lldp remote-device detail 0/10"]; !ok {
		t.Fatal(`LoadOutputs did not turn "0-10" back into "0/10"`)
	}
	return ParseNetgear("172.22.0.4", out)
}

func TestParseNetgearIdentity(t *testing.T) {
	sw := loadM4250(t)
	if sw.Model != "M4250-10G2F-PoE+" || sw.Firmware != "13.0.6.8" || sw.Serial == "" {
		t.Errorf("identity: %q %q %q", sw.Model, sw.Firmware, sw.Serial)
	}
	for _, m := range []string{"28:94:01:AF:35:BA", "28:94:01:AF:35:BD", "28:94:01:AF:35:BC"} {
		if !contains(sw.MACs, m) {
			t.Errorf("switch's own MAC %s missing from %v", m, sw.MACs)
		}
	}
}

func TestParseNetgearPorts(t *testing.T) {
	sw := loadM4250(t)
	if len(sw.Ports) != 12 {
		t.Fatalf("got %d ports, want the 12 physical ones (no lag or vlan rows)", len(sw.Ports))
	}
	p := map[string]Port{}
	for _, x := range sw.Ports {
		p[x.Name] = x
	}
	if !p["0/1"].Up || p["0/3"].Up {
		t.Error("link state: 0/1 should be up, 0/3 down")
	}
	if p["0/1"].Description != "VID:1,Default,Video with Dante audio" {
		t.Errorf("description not taken in full from show port description: %q", p["0/1"].Description)
	}
	if p["0/1"].PoEmW <= 0 || p["0/1"].PoEStatus != "Delivering Power" {
		t.Errorf("PoE on 0/1: %d mW %q", p["0/1"].PoEmW, p["0/1"].PoEStatus)
	}
	if p["0/1"].Speed != "1000 Full" || p["0/1"].VLAN != "1" {
		t.Errorf("speed/VLAN: %q %q", p["0/1"].Speed, p["0/1"].VLAN)
	}
	if len(p["0/10"].MACs) != 2 {
		t.Errorf("0/10 should have two learned MACs, got %v", p["0/10"].MACs)
	}
	for _, x := range sw.Ports {
		for _, e := range x.MACs {
			if strings.HasPrefix(e.MAC, "28:94:01:78:EA") {
				t.Errorf("switch's own MAC %s placed on port %s", e.MAC, x.Name)
			}
		}
	}
}

func TestParseNetgearLLDP(t *testing.T) {
	sw := loadM4250(t)
	var n Neighbor
	for _, x := range sw.Ports {
		if x.Name == "0/1" && len(x.Neighbors) == 1 {
			n = x.Neighbors[0]
		}
	}
	if n.SystemName != "TS-1080-UC-C44268F89D7B" || n.MgmtIP != "172.22.0.153" || n.ChassisID != "C4:42:68:F8:9D:7B" {
		t.Errorf("0/1 neighbor: %+v", n)
	}
	if n.CapsEnabled != "station only" {
		t.Errorf("capabilities enabled: %q", n.CapsEnabled)
	}
}

func TestBuildM4250(t *testing.T) {
	sw := loadM4250(t)
	snap := Build([]Switch{sw}, DeviceDB{}, OUI{}, nil, nil)
	if len(snap.Links) != 0 {
		t.Errorf("one switch, no links expected: %+v", snap.Links)
	}
	got := map[string][]string{}
	for _, d := range snap.Devices {
		got[d.Port] = append(got[d.Port], d.Name)
	}
	if strings.Join(got["0/1"], ",") != "TS-1080-UC-C44268F89D7B" {
		t.Errorf("0/1: %v", got["0/1"])
	}
	if strings.Join(got["0/2"], ",") != "U1-C44268DE4CC4" {
		t.Errorf("0/2: %v", got["0/2"])
	}
	// A PC with a VirtualBox VM bridged through it: two devices, the PC first, and no
	// "unmanaged switch" note, since a VM is not another box.
	if len(got["0/10"]) != 2 || got["0/10"][0] != "PC-01" {
		t.Errorf("0/10: %v", got["0/10"])
	}
	for _, d := range snap.Devices {
		if d.Port == "0/10" && d.Note != "" {
			t.Errorf("0/10 flagged: %q", d.Note)
		}
		if d.Port == "0/1" && d.Interfaces[0].IP != "172.22.0.153" {
			t.Errorf("panel IP from LLDP: %q", d.Interfaces[0].IP)
		}
	}
}

func TestAllowlist(t *testing.T) {
	for _, c := range []string{"show version", "show port description 0/5", "show lldp remote-device detail 1/0/12"} {
		if !Allowed(c) {
			t.Errorf("%q should be allowed", c)
		}
	}
	for _, c := range []string{"configure", "show port description 0/5; reload", "copy running-config startup-config",
		"show running-config", "reload", "show port description 0/5 x", "enable"} {
		if Allowed(c) {
			t.Errorf("%q must not be allowed", c)
		}
	}
}
