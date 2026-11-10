package netdiagram

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// CiscoCommands are run on a Cisco Catalyst 1300 / CBS350 (Cisco's small-business CLI), in this
// order. Per-port LLDP detail follows from "show lldp neighbors" (CiscoPortCommands).
//
// The syntax is Cisco's small-business CLI as internal/ciscossh speaks it; each command's output
// format is confirmed against the CLI guide and a real switch before its parser is trusted.
var CiscoCommands = []string{
	"show version",
	"show system",
	"show inventory",
	"show interfaces status",
	"show interfaces description",
	"show lldp neighbors",
	"show mac address-table",
	"show power inline",
	"show arp",
}

// CiscoPortCommands lists LLDP detail for each port "show lldp neighbors" names.
func CiscoPortCommands(out map[string]string) []string {
	var cmds []string
	seen := map[string]bool{}
	for _, line := range strings.Split(out["show lldp neighbors"], "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && anyPortWord.MatchString(f[0]) && !seen[f[0]] {
			seen[f[0]] = true
			cmds = append(cmds, "show lldp neighbors "+f[0])
		}
	}
	return cmds
}

// CollectCisco runs every Cisco command through run and returns their outputs. A command the
// switch refuses (ciscossh.CommandError) is kept as its error text rather than stopping the
// read, since models and firmware differ in what they offer (a switch without PoE has no
// "show power inline").
func CollectCisco(run func(string) (string, error)) (map[string]string, error) {
	out := map[string]string{}
	do := func(c string) error {
		o, err := run(c)
		if err != nil {
			if strings.HasPrefix(err.Error(), "refused to send") {
				return err
			}
			o = "% netdiagram: " + err.Error()
		}
		out[c] = o
		return nil
	}
	for _, c := range CiscoCommands {
		if err := do(c); err != nil {
			return out, err
		}
	}
	for _, c := range CiscoPortCommands(out) {
		if err := do(c); err != nil {
			return out, err
		}
	}
	return out, nil
}

// ParseCisco builds a Switch from a Catalyst 1300's command outputs. The formats are the CLI
// guide's examples (testdata/c1300-guide), not yet confirmed against a real switch.
func ParseCisco(host string, out map[string]string) (Switch, error) {
	sw := Switch{Host: host, Vendor: "Cisco"}
	if strings.HasPrefix(out["show system"], "% netdiagram:") || out["show interfaces status"] == "" {
		return sw, errors.New("no usable \"show system\" or \"show interfaces status\" output")
	}

	sys := colonFieldsFold(out["show system"])
	sw.Model = sys["system description"]
	sw.Name = sys["system name"]
	if m, ok := NormalizeMAC(sys["system mac address"]); ok {
		sw.MACs = append(sw.MACs, m)
	}
	// show inventory: NAME: "1", DESCR: "…" / PID: C1300-48P-4X, VID: V01, SN: …  (the first unit)
	if m := ciscoInventory.FindStringSubmatch(out["show inventory"]); m != nil {
		if pid := strings.TrimSpace(m[1]); pid != "" {
			sw.Model = pid
		}
		sw.Serial = strings.TrimSpace(m[2])
	}
	// show version: the version under Active-image.
	if i := strings.Index(out["show version"], "Active-image"); i >= 0 {
		if m := ciscoVersion.FindStringSubmatch(out["show version"][i:]); m != nil {
			sw.Firmware = m[1]
		}
	}

	ports := map[string]*Port{}
	var order []string
	for _, row := range tableUnder(out["show interfaces status"], "Port") {
		// Port, Type, Duplex, Speed, Neg, Flow ctrl, Link State, Back Pressure, Mdix Mode
		if len(row) < 7 || !anyPortWord.MatchString(row[0]) {
			continue
		}
		p := &Port{Name: row[0], Up: strings.EqualFold(row[6], "Up")}
		if p.Up && row[3] != "--" {
			p.Speed = strings.TrimSpace(row[3] + " " + row[2])
		}
		ports[p.Name] = p
		order = append(order, p.Name)
	}
	if len(order) == 0 {
		// Rather than draw an empty switch: the output isn't laid out as the CLI guide shows.
		return sw, errors.New("no ports found in \"show interfaces status\": its layout differs from the C1300 CLI guide's")
	}
	for _, row := range tableUnder(out["show interfaces description"], "Port") {
		if len(row) >= 2 && ports[row[0]] != nil {
			ports[row[0]].Description = row[1]
		}
	}

	for _, row := range tableUnder(out["show power inline"], "Interface") {
		// Interface, Admin, Oper, Power, Class, Device, Priority
		if len(row) < 4 || ports[row[0]] == nil {
			continue
		}
		p := ports[row[0]]
		p.PoEStatus = row[2]
		w := row[3]
		if i := strings.Index(w, "("); i >= 0 {
			w = w[:i] // "15.4(30)": 15.4 W drawn, 30 W the limit
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(w), 64); err == nil {
			p.PoEmW = int(f*1000 + 0.5)
		}
	}

	for _, row := range tableUnder(out["show mac address-table"], "VLAN") {
		// VLAN, MAC Address, Port, Type
		if len(row) < 4 {
			continue
		}
		mac, ok := NormalizeMAC(row[1])
		if !ok {
			continue
		}
		if strings.EqualFold(row[3], "self") {
			sw.MACs = append(sw.MACs, mac)
			continue
		}
		if p := ports[row[2]]; p != nil {
			p.MACs = append(p.MACs, MACEntry{MAC: mac, VLAN: row[0]})
		}
	}

	for name, p := range ports {
		d, ok := out["show lldp neighbors "+name]
		if !ok || strings.HasPrefix(d, "% netdiagram:") {
			continue
		}
		f := colonFieldsFold(d)
		n := Neighbor{
			ChassisID:   f["device id"],
			PortID:      f["port id"],
			SystemName:  f["system name"],
			SystemDesc:  f["system description"],
			PortDesc:    f["port description"],
			MgmtIP:      f["management address"],
			CapsEnabled: ciscoCaps(f["capabilities"]),
		}
		if n.ChassisID != "" {
			p.Neighbors = append(p.Neighbors, n)
		}
	}

	for _, row := range tableUnder(out["show arp"], "VLAN") {
		// VLAN, Interface, IP Address, HW Address, Status
		if len(row) < 4 {
			continue
		}
		if mac, ok := NormalizeMAC(row[3]); ok && row[2] != "" {
			sw.ARP = append(sw.ARP, ARP{MAC: mac, IP: row[2]})
		}
	}

	sw.MACs = uniq(sw.MACs)
	for _, name := range order {
		sw.Ports = append(sw.Ports, *ports[name])
	}
	return sw, nil
}

var (
	ciscoInventory = regexp.MustCompile(`PID:\s*([^,]*?)\s*,\s*VID:[^,]*,\s*SN:\s*(\S+)`)
	ciscoVersion   = regexp.MustCompile(`(?m)^\s*Version:\s*(\S+)`)
)

// ciscoCaps spells out LLDP capability letters ("B,R") the way Netgear writes them
// ("bridge, router"), so the switch-link logic reads both.
func ciscoCaps(s string) string {
	names := map[string]string{"B": "bridge", "R": "router", "W": "WLAN access point", "T": "telephone",
		"D": "DOCSIS cable device", "H": "host", "r": "repeater", "TP": "two-port MAC relay",
		"S": "S-VLAN", "C": "C-VLAN", "O": "other"}
	var out []string
	for _, c := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if n, ok := names[c]; ok {
			out = append(out, n)
		} else {
			out = append(out, c)
		}
	}
	return strings.Join(out, ", ")
}

// colonFieldsFold is colonFields with lower-case keys: Cisco writes "System Name" in the LLDP
// summary and "System description" in the detail.
func colonFieldsFold(out string) map[string]string {
	m := map[string]string{}
	for k, v := range colonFields(out) {
		k = strings.ToLower(k)
		if _, seen := m[k]; !seen {
			m[k] = v
		}
	}
	return m
}
