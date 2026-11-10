package netdiagram

import (
	"strings"
	"testing"
)

// Two switches cabled 0/12 ↔ 0/1, with devices that exercise placement and grouping.
func twoSwitches() []Switch {
	a := Switch{Host: "10.0.0.1", Vendor: "Netgear", MACs: []string{"28:94:01:00:00:01"}, Ports: []Port{
		{Name: "0/1", Up: true, MACs: []MACEntry{{MAC: "78:45:01:A5:22:23"}, {MAC: "78:45:01:A5:22:25"}}}, // Tesira, two interfaces
		{Name: "0/2", Up: true, MACs: []MACEntry{{MAC: "00:0E:DD:BE:89:9F"}, {MAC: "00:0E:DD:BE:89:A0"}}}, // MXA 920, in the database
		{Name: "0/3", Up: true, MACs: []MACEntry{{MAC: "AA:00:00:00:00:01"}, {MAC: "BB:00:00:00:00:02"}}}, // two unrelated boxes
		{Name: "0/12", Up: true,
			Neighbors: []Neighbor{{ChassisID: "28:94:01:00:00:02", SystemName: "B", CapsEnabled: "bridge"}},
			// Everything behind switch B is also learned here.
			MACs: []MACEntry{{MAC: "28:94:01:00:00:02"}, {MAC: "C4:42:68:00:00:10"}, {MAC: "DE:AD:00:00:00:01"}}},
		{Name: "0/11", Up: true, Neighbors: []Neighbor{{ChassisID: "11:22:33:44:55:66", SystemName: "closet-sw", CapsEnabled: "bridge"}},
			MACs: []MACEntry{{MAC: "11:22:33:44:55:66"}, {MAC: "CC:00:00:00:00:09"}}},
	}}
	b := Switch{Host: "10.0.0.2", Vendor: "Netgear", Name: "B", MACs: []string{"28:94:01:00:00:02"}, Ports: []Port{
		{Name: "0/1", Up: true, Neighbors: []Neighbor{{ChassisID: "28:94:01:00:00:01", CapsEnabled: "bridge"}},
			MACs: []MACEntry{{MAC: "28:94:01:00:00:01"}, {MAC: "78:45:01:A5:22:23"}}},
		{Name: "0/5", Up: true, MACs: []MACEntry{{MAC: "C4:42:68:00:00:10"}}},
	}}
	return []Switch{a, b}
}

func testDB() DeviceDB {
	return DeviceDB{
		"00:0E:DD:BE:89:9F": {MAC: "00:0E:DD:BE:89:9F", Name: "R101-Ceiling-Mic", Jobsite: "Example Job", Model: "MXA 920"},
		"00:0E:DD:BE:89:A0": {MAC: "00:0E:DD:BE:89:A0", Name: "R101-Ceiling-Mic-Dante", Jobsite: "Example Job", Model: "MXA 920"},
		"C4:42:68:00:00:10": {MAC: "C4:42:68:00:00:10", Name: "Rack-Encoder", Model: "NVX E30"},
	}
}

func byMAC(snap Snapshot, mac string) *Device {
	for i, d := range snap.Devices {
		for _, in := range d.Interfaces {
			if in.MAC == mac {
				return &snap.Devices[i]
			}
		}
	}
	return nil
}

func TestLinksBetweenSwitches(t *testing.T) {
	snap := Build(twoSwitches(), testDB(), OUI{}, nil, nil)
	var read, unread int
	for _, l := range snap.Links {
		if l.ToSwitch != "" {
			read++
			if !((l.FromPort == "0/12" && l.ToPort == "0/1") || (l.FromPort == "0/1" && l.ToPort == "0/12")) {
				t.Errorf("link ports: %+v", l)
			}
		} else {
			unread++
			if l.ToName != "closet-sw" {
				t.Errorf("unread switch name: %+v", l)
			}
		}
	}
	if read != 1 || unread != 1 {
		t.Errorf("want one link between A and B (seen from both ends, kept once) and one to closet-sw, got %+v", snap.Links)
	}
}

// A switch whose MAC the database lists is drawn and tabled under the database's name, while its
// system name (what LLDP neighbors know it by) is kept, so the links are still found.
func TestSwitchNamedFromDatabase(t *testing.T) {
	db := testDB()
	db["28:94:01:00:00:01"] = DBEntry{MAC: "28:94:01:00:00:01", Name: "LAB-101-SW-001"}
	snap := Build(twoSwitches(), db, OUI{}, nil, nil)
	a := snap.Switches[0]
	if a.DBName != "LAB-101-SW-001" || a.Title() != "LAB-101-SW-001" {
		t.Errorf("switch A: db_name %q, title %q", a.DBName, a.Title())
	}
	if b := snap.Switches[1]; b.DBName != "" || b.Title() == "" {
		t.Errorf("switch B isn't in the database: db_name %q, title %q", b.DBName, b.Title())
	}
	read := 0
	for _, l := range snap.Links {
		if l.ToSwitch != "" {
			read++
		}
	}
	if read != 1 {
		t.Errorf("the A-B link was lost: %+v", snap.Links)
	}
	if !strings.Contains(SVG(snap), `class="swname">LAB-101-SW-001<`) {
		t.Error("the switch box isn't titled with the database name")
	}
	for _, d := range snap.Devices {
		if d.Name == "LAB-101-SW-001" {
			t.Errorf("the switch's own MAC became a device: %+v", d)
		}
	}
}

func TestDevicePlacedOnItsEdgePort(t *testing.T) {
	snap := Build(twoSwitches(), testDB(), OUI{}, nil, nil)
	enc := byMAC(snap, "C4:42:68:00:00:10")
	if enc == nil || enc.Switch != "10.0.0.2" || enc.Port != "0/5" || enc.Beyond {
		t.Errorf("encoder should be on B 0/5 (A 0/12 is an uplink): %+v", enc)
	}
	tes := byMAC(snap, "78:45:01:A5:22:25")
	if tes == nil || tes.Switch != "10.0.0.1" || tes.Port != "0/1" {
		t.Errorf("Tesira on A 0/1: %+v", tes)
	}
	// Seen only on the uplink to a switch that wasn't read.
	c := byMAC(snap, "CC:00:00:00:00:09")
	if c == nil || !c.Beyond || c.Port != "0/11" {
		t.Errorf("device behind closet-sw: %+v", c)
	}
	if byMAC(snap, "28:94:01:00:00:02") != nil {
		t.Error("a read switch's own MAC became a device")
	}
}

func TestGrouping(t *testing.T) {
	snap := Build(twoSwitches(), testDB(), OUI{}, nil, nil)
	tes := byMAC(snap, "78:45:01:A5:22:23")
	if tes == nil || len(tes.Interfaces) != 2 || tes.Grouping != "likely" {
		t.Errorf("Tesira's two MACs, 2 apart, should be one likely device: %+v", tes)
	}
	mic := byMAC(snap, "00:0E:DD:BE:89:9F")
	if mic == nil || len(mic.Interfaces) != 2 || mic.Name != "R101-Ceiling-Mic" {
		t.Errorf("MXA: one device named from the shared start of its names: %+v", mic)
	}
	a, b := byMAC(snap, "AA:00:00:00:00:01"), byMAC(snap, "BB:00:00:00:00:02")
	if a == nil || b == nil || a == b {
		t.Fatal("unrelated MACs on one port must stay two devices")
	}
	if !strings.Contains(a.Note, "2 devices on this port") {
		t.Errorf("two boxes on one port should be noted: %q", a.Note)
	}
}

func TestCommonName(t *testing.T) {
	cases := map[string][]string{
		"Rack-Tesira": {"Rack-Tesira", "Rack-Tesira-Dante"},
		"Rack DSP":      {"Rack DSP control", "Rack DSP Dante"},
		"Alpha":         {"Alpha", "Zulu"},
	}
	for want, names := range cases {
		if got := commonName(names); got != want {
			t.Errorf("commonName(%v) = %q, want %q", names, got, want)
		}
	}
}

func TestNormalizeMAC(t *testing.T) {
	for _, in := range []string{"c442.6812.3456", "C4-42-68-12-34-56", "c44268123456", "C4:42:68:12:34:56"} {
		if m, ok := NormalizeMAC(in); !ok || m != "C4:42:68:12:34:56" {
			t.Errorf("NormalizeMAC(%q) = %q %v", in, m, ok)
		}
	}
	if _, ok := NormalizeMAC("CPU Interface:"); ok {
		t.Error("non-MAC accepted")
	}
}

func TestColumnTableEmptyCells(t *testing.T) {
	rows := columnTable("A    B     C\n---  ----  ---\n0/1        x\n0/2  yy    z\n")
	if len(rows) != 2 || rows[0][0] != "0/1" || rows[0][1] != "" || rows[0][2] != "x" || rows[1][1] != "yy" {
		t.Errorf("rows: %q", rows)
	}
}

// crosses reports whether two wires drawn with wireLevels' heights touch: a vertical drop of one
// passing through the other's horizontal run, or two runs overlapping on one height.
func crosses(from, to []float64, lv []int, a, b int) bool {
	lo := func(i int) float64 { return min(from[i], to[i]) }
	hi := func(i int) float64 { return max(from[i], to[i]) }
	inside := func(x float64, i int) bool { return x > lo(i) && x < hi(i) }
	if lv[a] == lv[b] && lo(a) <= hi(b) && lo(b) <= hi(a) && from[a] != to[a] && from[b] != to[b] {
		return true
	}
	for _, p := range [][2]int{{a, b}, {b, a}} {
		x, y := p[0], p[1]
		if inside(from[x], y) && lv[y] < lv[x] { // x's drop from the switch reaches below y's run
			return true
		}
		if inside(to[x], y) && lv[y] > lv[x] { // x's drop to its device starts above y's run
			return true
		}
	}
	return false
}

func TestWireLevelsBench(t *testing.T) {
	// The test M4250: ports 1, 2 and 10 to the first three columns.
	from := []float64{59, 85, 293}
	to := []float64{125, 315, 505}
	lv, _ := wireLevels(from, to)
	for a := 0; a < 3; a++ {
		for b := a + 1; b < 3; b++ {
			if crosses(from, to, lv, a, b) {
				t.Errorf("wires %d and %d cross with levels %v", a, b, lv)
			}
		}
	}
}

func TestWireLevelsMixedDirections(t *testing.T) {
	// A 48-port switch: the first wires run right to their columns, the later ones left.
	var from, to []float64
	ports := []int{1, 2, 5, 20, 30, 40, 44, 47, 48}
	for i, p := range ports {
		from = append(from, float64(50+p*26))
		to = append(to, float64(125+i*190))
	}
	lv, _ := wireLevels(from, to)
	for a := range from {
		for b := a + 1; b < len(from); b++ {
			if crosses(from, to, lv, a, b) {
				t.Errorf("wires %d and %d cross with levels %v", a, b, lv)
			}
		}
	}
}

func TestYourComputer(t *testing.T) {
	sw := loadM4250(t)
	// The PC on 0/10, with a VirtualBox VM bridged through it.
	self := map[string][]string{"7C:CF:0F:78:FD:37": {"172.22.0.248"}, "00:11:22:33:44:55": nil}
	snap := Build([]Switch{sw}, DeviceDB{}, OUI{}, nil, self)
	var pc, vm *Device
	for i, d := range snap.Devices {
		switch d.Interfaces[0].MAC {
		case "7C:CF:0F:78:FD:37":
			pc = &snap.Devices[i]
		case "08:00:27:B8:B8:C7":
			vm = &snap.Devices[i]
		}
	}
	if pc == nil || pc.Name != "Your Computer" || !pc.Self || pc.Model != "PC-01" {
		t.Errorf("the PC: %+v (its LLDP name belongs on the second line)", pc)
	}
	if vm == nil || vm.Self || vm.Name == "Your Computer" {
		t.Errorf("the VM bridged through the PC is not the PC: %+v", vm)
	}
}
