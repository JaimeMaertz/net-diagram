package netdiagram

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// NetgearCommands are run on every Netgear M4250, in this order. Per-port commands follow from
// their answers (NetgearPortCommands).
var NetgearCommands = []string{
	"show version",
	"show sysinfo",
	"show interfaces status all",
	"show lldp remote-device all",
	"show mac-addr-table all",
	"show poe port info all",
	"show arp switch",
}

var netgearPort = regexp.MustCompile(`^\d+/\d+$`)

// savedPort is a port argument as SaveOutputs writes it into a file name.
var savedPort = regexp.MustCompile(`^[A-Za-z]*\d+(-\d+){1,2}$`)

// NetgearPortCommands lists the per-port commands the first answers call for: each physical
// port's full description, and LLDP detail for each port with a neighbor.
func NetgearPortCommands(out map[string]string) []string {
	var cmds []string
	for _, row := range columnTable(out["show interfaces status all"]) {
		if netgearPort.MatchString(row[0]) {
			cmds = append(cmds, "show port description "+row[0])
		}
	}
	for _, row := range columnTable(out["show lldp remote-device all"]) {
		if netgearPort.MatchString(row[0]) && len(row) > 1 && row[1] != "" {
			cmds = append(cmds, "show lldp remote-device detail "+row[0])
		}
	}
	return cmds
}

// CollectNetgear runs every Netgear command on a session and returns their outputs.
func CollectNetgear(run func(string) (string, error)) (map[string]string, error) {
	out := map[string]string{}
	for _, c := range NetgearCommands {
		o, err := run(c)
		if err != nil {
			return out, err
		}
		out[c] = o
	}
	for _, c := range NetgearPortCommands(out) {
		o, err := run(c)
		if err != nil {
			return out, err
		}
		out[c] = o
	}
	return out, nil
}

// LoadOutputs reads a folder of saved command outputs, as cmd/probe writes them
// ("show version" → show_version.txt, "0/5" → "0-5").
func LoadOutputs(dir string) (map[string]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "show_*.txt"))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(filepath.Base(f), ".txt")
		cmd := strings.ReplaceAll(name, "_", " ")
		// Port arguments were written "0-5" or "gi1-0-5": turn the last word back into "0/5", "gi1/0/5".
		if i := strings.LastIndex(cmd, " "); i >= 0 && savedPort.MatchString(cmd[i+1:]) {
			cmd = cmd[:i+1] + strings.ReplaceAll(cmd[i+1:], "-", "/")
		}
		out[cmd] = string(b)
	}
	return out, nil
}

// ParseNetgear builds a Switch from a Netgear M4250's command outputs.
func ParseNetgear(host string, out map[string]string) Switch {
	sw := Switch{Host: host, Vendor: "Netgear"}

	v := dotted(out["show version"])
	sw.Model = v["Machine Model"]
	sw.Firmware = v["Software Version"]
	sw.Serial = v["Serial Number"]
	if m, ok := NormalizeMAC(v["Burned In MAC Address"]); ok {
		sw.MACs = append(sw.MACs, m)
	}
	sw.Name = dotted(out["show sysinfo"])["System Name"]

	ports := map[string]*Port{}
	var order []string
	for _, row := range columnTable(out["show interfaces status all"]) {
		// Port, Name, Link State, Physical Mode, Physical Status, Media Type, Flow Control, VLAN
		if len(row) < 8 || !netgearPort.MatchString(row[0]) {
			continue
		}
		p := &Port{Name: row[0], Description: row[1], Up: row[2] == "Up", Speed: row[4], VLAN: row[7]}
		ports[p.Name] = p
		order = append(order, p.Name)
	}
	for name, p := range ports {
		d := dotted(out["show port description "+name])
		if desc := d["Description"]; desc != "" {
			p.Description = desc
		}
		if m, ok := NormalizeMAC(d["MAC address"]); ok {
			sw.MACs = append(sw.MACs, m)
		}
	}

	for _, row := range columnTable(out["show poe port info all"]) {
		// Intf, High Power, Max Power, Class, Power (mW), Current, Voltage, Status, Fault Status
		if len(row) < 9 {
			continue
		}
		if p := ports[row[0]]; p != nil {
			p.PoEmW, _ = strconv.Atoi(row[4])
			p.PoEStatus = row[7]
		}
	}

	for _, row := range columnTable(out["show mac-addr-table all"]) {
		// VLAN ID, MAC Address, Interface, IfIndex, Status
		if len(row) < 5 {
			continue
		}
		mac, ok := NormalizeMAC(row[1])
		if !ok {
			continue
		}
		if row[4] == "Management" {
			sw.MACs = append(sw.MACs, mac)
			continue
		}
		if p := ports[row[2]]; p != nil {
			p.MACs = append(p.MACs, MACEntry{MAC: mac, VLAN: row[0]})
		}
	}

	for name, p := range ports {
		d, ok := out["show lldp remote-device detail "+name]
		if !ok {
			continue
		}
		f := colonFields(d)
		n := Neighbor{
			ChassisID:   f["Chassis ID"],
			PortID:      f["Port ID"],
			SystemName:  f["System Name"],
			SystemDesc:  f["System Description"],
			PortDesc:    f["Port Description"],
			CapsEnabled: f["System Capabilities Enabled"],
		}
		if m := regexp.MustCompile(`(?m)^\s*Address:\s*(\d+\.\d+\.\d+\.\d+)`).FindStringSubmatch(d); m != nil {
			n.MgmtIP = m[1]
		}
		if n.ChassisID != "" {
			p.Neighbors = append(p.Neighbors, n)
		}
	}

	for _, row := range columnTable(out["show arp switch"]) {
		// MAC Address, IP Address, Interface
		if len(row) < 2 {
			continue
		}
		if mac, ok := NormalizeMAC(row[0]); ok && row[1] != "" {
			sw.ARP = append(sw.ARP, ARP{MAC: mac, IP: row[1]})
		}
	}

	sw.MACs = uniq(sw.MACs)
	for _, name := range order {
		sw.Ports = append(sw.Ports, *ports[name])
	}
	return sw
}

func uniq(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
