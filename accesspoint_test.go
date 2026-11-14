package netdiagram

import (
	"reflect"
	"strings"
	"testing"
)

// buildChain is the three TP-Link switches as read, mapped with no device database and the
// manufacturer list the test needs (the fixtures' makers by their first three bytes).
func buildChain(t *testing.T) Snapshot {
	t.Helper()
	var sw []Switch
	for i, d := range []string{"tl-sg3428xmp", "tl-sg3428x", "tl-sg3428x-b"} {
		sw = append(sw, loadTPLink(t, d, []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}[i]))
	}
	oui := OUI{"B4FBE4": "Ubiquiti Inc", "B0BE76": "TP-LINK TECHNOLOGIES CO.,LTD.", "306893": "TP-Link Systems Inc.",
		"98BA5F": "TP-Link Systems Inc.", "242FD0": "TP-Link Systems Inc"}
	return Build(sw, DeviceDB{}, oui, nil, nil)
}

// onPort is the device on the XMP's port whose first MAC starts with prefix (a maker block, which the
// fixtures' MAC scrub keeps).
func onPort(t *testing.T, s Snapshot, port, prefix string) Device {
	t.Helper()
	for _, d := range s.Devices {
		if d.Switch == "10.0.0.1" && d.Port == port && strings.HasPrefix(d.Interfaces[0].MAC, prefix) {
			return d
		}
	}
	t.Fatalf("no device %s… on %s", prefix, port)
	return Device{}
}

// On the network the fixtures came from, the XMP's ports 9, 11 and 13 each have one access point
// (a UniFi, two TP-Link) wired straight to them, and everything else on those ports goes through it.
func TestMarkAccessPointsFromFingerprints(t *testing.T) {
	s := buildChain(t)
	aps := map[string]string{"Gi1/0/9": "B4:FB:E4", "Gi1/0/11": "B0:BE:76", "Gi1/0/13": "30:68:93"}
	fp := map[string]string{}
	for port, prefix := range aps {
		fp[onPort(t, s, port, prefix).Interfaces[0].MAC] = "a test signature"
	}
	MarkAccessPoints(&s, DeviceDB{}, fp)
	for port, prefix := range aps {
		ap := onPort(t, s, port, prefix)
		if ap.AccessPoint != "likely: a test signature" || ap.Via != "" {
			t.Errorf("%s: access point %q via %q", port, ap.AccessPoint, ap.Via)
		}
		first := true
		n := 0
		for _, d := range s.Devices {
			if d.Switch != "10.0.0.1" || d.Port != port {
				continue
			}
			if first && d.AccessPoint == "" {
				t.Errorf("%s: the access point doesn't head its port's devices", port)
			}
			first = false
			if d.AccessPoint == "" {
				n++
				if d.Via != ap.Interfaces[0].MAC || !strings.HasPrefix(d.Note, "through "+ap.Name) {
					t.Errorf("%s: %s via %q note %q", port, d.Name, d.Via, d.Note)
				}
			}
		}
		if n == 0 || !strings.HasPrefix(ap.Note, "access point: ") || !strings.Contains(ap.Note, "likely (a test signature)") {
			t.Errorf("%s: %d through it, note %q", port, n, ap.Note)
		}
	}
	// A port with several devices and no access point keeps the unmanaged-switch note.
	if d := onPort(t, s, "Gi1/0/7", "BC:24:11"); d.Via != "" || !strings.Contains(d.Note, "unmanaged switch") {
		t.Errorf("Gi1/0/7: via %q note %q", d.Via, d.Note)
	}
	// Marking again with the same evidence changes nothing.
	again := s
	again.Devices = append([]Device(nil), s.Devices...)
	MarkAccessPoints(&again, DeviceDB{}, fp)
	if !reflect.DeepEqual(again.Devices, s.Devices) {
		t.Error("a second MarkAccessPoints changed the devices")
	}
	// The drawing hangs the devices beneath their access point on a connector.
	svg := SVG(s)
	if strings.Count(svg, `class="via"`) < 15 || !strings.Contains(svg, `class="dev ap"`) {
		t.Errorf("drawing: %d connectors, access point box %v", strings.Count(svg, `class="via"`), strings.Contains(svg, `class="dev ap"`))
	}
}

func TestMarkAccessPointsFromLLDPAndDatabase(t *testing.T) {
	mk := func(port string, macs ...string) []Device {
		var ds []Device
		for _, m := range macs {
			ds = append(ds, Device{Name: m, Switch: "S", Port: port, Interfaces: []Interface{{MAC: m}}})
		}
		return ds
	}
	const ap1, c1, ap2, c2, ap3, ap4, c3 = "02:00:00:00:01:01", "02:00:00:00:01:02", "02:00:00:00:02:01", "02:00:00:00:02:02",
		"02:00:00:00:03:01", "02:00:00:00:03:02", "02:00:00:00:03:03"
	s := Snapshot{Switches: []Switch{{Host: "S", Ports: []Port{{Name: "1", Neighbors: []Neighbor{{ChassisID: ap1, CapsEnabled: "bridge, wlan access point"}}}}}}}
	s.Devices = append(s.Devices, mk("1", c1, ap1)...)
	s.Devices = append(s.Devices, mk("2", ap2, c2)...)
	s.Devices = append(s.Devices, mk("3", ap3, ap4, c3)...)
	db := DeviceDB{ap2: {Type: "Access Point"}, ap3: {Type: "AP"}, ap4: {Type: "wap"}}
	MarkAccessPoints(&s, db, nil)
	byMAC := map[string]Device{}
	for _, d := range s.Devices {
		byMAC[d.Interfaces[0].MAC] = d
	}
	if byMAC[ap1].AccessPoint != "LLDP" || byMAC[c1].Via != ap1 || s.Devices[0].Interfaces[0].MAC != ap1 {
		t.Errorf("LLDP: %+v / %+v, first on the port %s", byMAC[ap1], byMAC[c1], s.Devices[0].Interfaces[0].MAC)
	}
	if byMAC[ap2].AccessPoint != "device database" || byMAC[c2].Via != ap2 {
		t.Errorf("database: %+v / %+v", byMAC[ap2], byMAC[c2])
	}
	// Two access points on one port: both marked, nothing hung beneath either.
	if byMAC[ap3].AccessPoint == "" || byMAC[ap4].AccessPoint == "" || byMAC[c3].Via != "" || !strings.Contains(byMAC[c3].Note, "2 access points") {
		t.Errorf("two on one port: %+v / %+v / %+v", byMAC[ap3], byMAC[ap4], byMAC[c3])
	}
}

func TestMarkAccessPointsBeyondUplink(t *testing.T) {
	s := Snapshot{Devices: []Device{
		{Name: "a", Switch: "S", Port: "U", Beyond: true, Interfaces: []Interface{{MAC: "02:00:00:00:04:01"}}},
		{Name: "b", Switch: "S", Port: "U", Beyond: true, Interfaces: []Interface{{MAC: "02:00:00:00:04:02"}}},
	}}
	MarkAccessPoints(&s, DeviceDB{}, map[string]string{"02:00:00:00:04:01": "x"})
	if s.Devices[1].Via != "" || s.Devices[0].Note != "" {
		t.Errorf("devices beyond an uplink were hung under an access point: %+v", s.Devices)
	}
}

func TestFingerprintAPs(t *testing.T) {
	banners := map[string]string{"10.0.0.11": "dropbear_2012.55", "10.0.0.12": "IPSSH-6.6.0"}
	servers := map[string]string{"10.0.0.13": "TP-LINK HTTPD/1.0", "10.0.0.14": "SHIP 2.0", "10.0.0.15": "Web Switch"}
	readBanner = func(ip string) string { return banners[ip] }
	readServer = func(ip string) string { return servers[ip] }
	defer func() { readBanner = func(ip string) string { return SSHBanner(ip, 2e9) }; readServer = webServer }()
	dev := func(name, maker, mac, ip string) Device {
		return Device{Name: name, Manufacturer: maker, Switch: "S", Port: "1", Interfaces: []Interface{{MAC: mac, IP: ip}}}
	}
	s := Snapshot{Devices: []Device{
		dev("unifi", "Ubiquiti Inc", "02:00:00:00:05:11", "10.0.0.11"),
		dev("ubnt-switch-ish", "Ubiquiti Inc", "02:00:00:00:05:12", "10.0.0.12"),
		dev("eap", "TP-Link Systems Inc.", "02:00:00:00:05:13", "10.0.0.13"),
		dev("plug", "TP-Link Systems Inc.", "02:00:00:00:05:14", "10.0.0.14"),
		dev("switch", "TP-Link Systems Inc", "02:00:00:00:05:15", "10.0.0.15"),
		dev("other maker", "Sonos, Inc.", "02:00:00:00:05:16", "10.0.0.11"),
		{Name: "alone", Manufacturer: "Ubiquiti Inc", Switch: "S", Port: "2", Interfaces: []Interface{{MAC: "02:00:00:00:05:17", IP: "10.0.0.11"}}},
	}}
	got := FingerprintAPs(s)
	want := map[string]string{"02:00:00:00:05:11": "Ubiquiti, SSH dropbear", "02:00:00:00:05:13": "TP-Link, web server TP-LINK HTTPD"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fingerprints:\n got %v\nwant %v", got, want)
	}
}
