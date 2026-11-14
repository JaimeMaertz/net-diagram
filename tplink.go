package netdiagram

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// TPLinkCommands are run on a TP-Link JetStream switch (TL-SG3428X, TL-SG3428XMP), in this order.
// Each is read-only and was taken from the switch's own help. "show lldp" is the global LLDP status:
// neighbors are asked for only when it says Enabled (TPLinkLLDPCommands).
var TPLinkCommands = []string{
	"show system-info",
	"show interface status",
	"show mac address-table",
	"show power inline information interface",
	"show arp",
	"show lldp",
}

// tplinkError matches the CLI's refusal: a caret line, then "Error: Bad command" (or "Incomplete
// command", "Invalid input"), printed as ordinary output.
var tplinkError = regexp.MustCompile(`(?m)^Error: (.+)$`)

// CollectTPLink runs every TP-Link command through run and returns their outputs. A command the
// switch refuses (a TL-SG3428X has no PoE) is kept as "% netdiagram: <its error>", as on Cisco.
func CollectTPLink(run func(string) (string, error)) (map[string]string, error) {
	out := map[string]string{}
	do := func(c string) error {
		o, err := run(c)
		if err != nil {
			return fmt.Errorf("%s: %w", c, err)
		}
		if m := tplinkError.FindStringSubmatch(o); m != nil {
			o = "% netdiagram: " + strings.TrimSpace(m[1])
		}
		out[c] = o
		return nil
	}
	for _, c := range TPLinkCommands {
		if err := do(c); err != nil {
			return out, err
		}
	}
	for _, c := range TPLinkLLDPCommands(out) {
		if err := do(c); err != nil {
			return out, err
		}
	}
	return out, nil
}

// TPLinkLLDPCommands lists the per-port neighbor reads when LLDP is on: one per port that is up.
// Firmware 1.0.7 refuses a bare "show lldp neighbor-information"; the per-port form works on both
// firmware lines seen.
func TPLinkLLDPCommands(out map[string]string) []string {
	if !regexp.MustCompile(`(?i)LLDP Status:\s*Enabled`).MatchString(out["show lldp"]) {
		return nil
	}
	var cmds []string
	for _, row := range tableUnder(out["show interface status"], "Port") {
		if len(row) >= 2 && strings.EqualFold(row[1], "LinkUp") {
			if long := tplinkLongPort(row[0]); long != "" {
				cmds = append(cmds, "show lldp neighbor-information interface "+long)
			}
		}
	}
	return cmds
}

// tplinkLongPort spells a short port name ("Gi1/0/5") as the CLI's interface argument
// ("gigabitEthernet 1/0/5").
func tplinkLongPort(short string) string {
	m := regexp.MustCompile(`^(Fa|Gi|Tw|Te)(\d+/\d+/\d+)$`).FindStringSubmatch(short)
	if m == nil {
		return ""
	}
	return map[string]string{"Fa": "fastEthernet", "Gi": "gigabitEthernet", "Tw": "two-gigabitEthernet", "Te": "ten-gigabitEthernet"}[m[1]] + " " + m[2]
}

// tplinkFields reads "Key   - value" lines (show system-info) into a map keyed by the lowercased key.
func tplinkFields(out string) map[string]string {
	f := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if k, v, ok := strings.Cut(line, " - "); ok && strings.TrimSpace(k) != "" {
			f[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	return f
}

// tableWith is the rows of the first table whose header line holds every word: TP-Link's MAC table
// has a title ("MAC Address Table") on its own dashed line above the real header.
func tableWith(out string, words ...string) [][]string {
	for _, t := range columnTables(out) {
		h := strings.ToLower(t.header)
		all := true
		for _, w := range words {
			all = all && strings.Contains(h, strings.ToLower(w))
		}
		if all {
			return t.rows
		}
	}
	return nil
}

var tplinkSpeed = regexp.MustCompile(`^(\d+)M$`)

// ParseTPLink builds a Switch from a TP-Link JetStream switch's command outputs, including each port's
// LLDP neighbors when LLDP was on.
func ParseTPLink(host string, out map[string]string) (Switch, error) {
	sw := Switch{Host: host, Vendor: "TP-Link"}
	if strings.HasPrefix(out["show system-info"], "% netdiagram:") || out["show interface status"] == "" {
		return sw, errors.New("no usable \"show system-info\" or \"show interface status\" output")
	}

	sys := tplinkFields(out["show system-info"])
	sw.Name = sys["system name"]
	// "TL-SG3428XMP 3.0": the model and its hardware version.
	if f := strings.Fields(sys["hardware version"]); len(f) > 0 {
		sw.Model = f[0]
	}
	if sw.Model == "" {
		sw.Model = sys["system description"]
	}
	// "3.0.3 Build 20230602 Rel.76586": the version and its build.
	if f := strings.Fields(sys["software version"]); len(f) >= 3 && f[1] == "Build" {
		sw.Firmware = f[0] + " Build " + f[2]
	} else {
		sw.Firmware = sys["software version"]
	}
	sw.Serial = sys["serial number"]
	if m, ok := NormalizeMAC(sys["mac address"]); ok {
		sw.MACs = append(sw.MACs, m)
	}

	ports := map[string]*Port{}
	var order []string
	for _, row := range tableUnder(out["show interface status"], "Port") {
		// Port, Status, Speed, Duplex, FlowCtrl, Active-Medium, Description
		if len(row) < 4 || !anyPortWord.MatchString(row[0]) {
			continue
		}
		p := &Port{Name: row[0], Up: strings.EqualFold(row[1], "LinkUp")}
		if p.Up && row[2] != "N/A" {
			p.Speed = strings.TrimSpace(tplinkSpeed.ReplaceAllString(row[2], "$1") + " " + row[3])
		}
		if len(row) >= 7 {
			p.Description = row[6]
		}
		ports[p.Name] = p
		order = append(order, p.Name)
	}
	if len(order) == 0 {
		return sw, errors.New("no ports found in \"show interface status\": its layout differs from the TL-SG3428X's")
	}

	for _, row := range tableUnder(out["show power inline information interface"], "Interface") {
		// Interface, Power(w), Current(mA), Voltage(v), PD-Class, Power-Status
		if len(row) < 6 || ports[row[0]] == nil {
			continue
		}
		p := ports[row[0]]
		p.PoEStatus = row[5]
		if f, err := strconv.ParseFloat(row[1], 64); err == nil {
			p.PoEmW = int(f*1000 + 0.5)
		}
	}

	for _, row := range tableWith(out["show mac address-table"], "MAC", "VLAN", "Port") {
		// MAC, VLAN, Port, Type, Aging
		if len(row) < 3 {
			continue
		}
		mac, ok := NormalizeMAC(row[0])
		if !ok {
			continue
		}
		if p := ports[row[2]]; p != nil {
			p.MACs = append(p.MACs, MACEntry{MAC: mac, VLAN: row[1]})
		}
	}

	for name, p := range ports {
		if long := tplinkLongPort(name); long != "" {
			p.Neighbors = parseTPLinkNeighbors(out["show lldp neighbor-information interface "+long])
		}
	}

	// show arp has no dashed line: "VLAN1  192.168.1.22  00:11:22:33:44:55  DYNAMIC".
	for _, line := range strings.Split(out["show arp"], "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !isIPv4(f[1]) {
			continue
		}
		if mac, ok := NormalizeMAC(f[2]); ok {
			sw.ARP = append(sw.ARP, ARP{MAC: mac, IP: f[1]})
		}
	}

	sw.MACs = uniq(sw.MACs)
	for _, name := range order {
		sw.Ports = append(sw.Ports, *ports[name])
	}
	return sw, nil
}

var ipv4 = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}$`)

// isIPv4 reports whether s is a dotted IPv4 address.
func isIPv4(s string) bool { return ipv4.MatchString(s) }

var (
	tplinkNeighborStart = regexp.MustCompile(`^Neighbor index \d+:`)
	tplinkKeyValue      = regexp.MustCompile(`^\s+([A-Za-z][A-Za-z0-9 ()/-]*?):\s*(.*)$`)
)

// parseTPLinkNeighbors reads one port's "show lldp neighbor-information interface …": a block per
// "Neighbor index N:" of "Key:   value" lines, where a long value (the system description) wraps at a
// fixed width, mid-word, onto lines that hold only the rest of the value. "No Neighbor." gives none.
func parseTPLinkNeighbors(out string) []Neighbor {
	if strings.HasPrefix(out, "% netdiagram:") {
		return nil
	}
	var blocks []map[string]string
	var cur map[string]string
	last := ""
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if tplinkNeighborStart.MatchString(strings.TrimSpace(line)) {
			cur = map[string]string{}
			blocks = append(blocks, cur)
			last = ""
			continue
		}
		if cur == nil || strings.TrimSpace(line) == "" {
			continue
		}
		if m := tplinkKeyValue.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, strings.Repeat(" ", 40)) {
			last = strings.ToLower(strings.TrimSpace(m[1]))
			cur[last] = strings.TrimSpace(m[2])
		} else if last != "" {
			cur[last] += strings.TrimSpace(line) // the wrapped rest of the value
		}
	}
	var ns []Neighbor
	for _, b := range blocks {
		n := Neighbor{
			ChassisID:   b["chassis id"],
			PortID:      b["port id"],
			PortDesc:    b["port description"],
			SystemName:  b["system name"],
			SystemDesc:  b["system description"],
			MgmtIP:      b["management address"],
			CapsEnabled: tplinkCaps(b["system capabilities enabled"]),
		}
		if n.ChassisID != "" {
			ns = append(ns, n)
		}
	}
	return ns
}

// lldpCaps are LLDP's system capabilities (IEEE 802.1AB), longest first so "WLAN Access Point" isn't read
// as a word at a time.
var lldpCaps = []string{"DOCSIS cable device", "WLAN Access Point", "Station Only", "Repeater", "Bridge", "Router", "Telephone", "Other"}

// tplinkCaps turns TP-Link's space-separated capabilities ("Bridge Router ", "Station Only ") into the
// comma list the rest of net-diagram reads ("bridge, router", "station only").
func tplinkCaps(s string) string {
	rest := " " + strings.Join(strings.Fields(s), " ") + " "
	var out []string
	for _, c := range lldpCaps {
		if i := strings.Index(strings.ToLower(rest), " "+strings.ToLower(c)+" "); i >= 0 {
			out = append(out, strings.ToLower(c))
			rest = rest[:i] + " " + rest[i+len(c)+1:]
		}
	}
	for _, w := range strings.Fields(rest) {
		out = append(out, strings.ToLower(w)) // a name not in the standard list, kept as it came
	}
	order := map[string]int{}
	for i, w := range strings.Fields(strings.ToLower(s)) {
		if _, ok := order[w]; !ok {
			order[w] = i
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return order[strings.Fields(out[i])[0]] < order[strings.Fields(out[j])[0]] })
	return strings.Join(out, ", ")
}
