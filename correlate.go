package netdiagram

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// likelyGap is how far apart two MACs from the same manufacturer may be and still be taken for
// interfaces of one box (the Tesira X400's control and Dante MACs are 2 apart, an MXA 920's 1).
const likelyGap = 8

// virtualOUI are MAC blocks used by virtual machines: a VM bridged through its host's cable is
// another MAC on that port, not another box.
var virtualOUI = map[string]string{
	"080027": "VirtualBox",
	"000C29": "VMware",
	"005056": "VMware",
	"00155D": "Hyper-V",
	"525400": "QEMU/KVM",
}

// Build works out the map from what the switches told us: switch-to-switch links, which port
// each device is on, and which MACs are one device. extraARP adds MAC→IP pairs seen elsewhere
// (a sweep from this PC). self is the PC's own adapters (LocalMACs): the device holding them is
// named "Your Computer".
func Build(switches []Switch, db DeviceDB, oui OUI, extraARP []ARP, self map[string][]string) Snapshot {
	snap := Snapshot{Taken: time.Now(), Switches: switches}
	// A switch is named from the database by whichever of its MACs it lists (usually the
	// management one), kept apart from the system name its neighbors know it by.
	for i := range snap.Switches {
		for _, m := range snap.Switches[i].MACs {
			if e, ok := db[m]; ok && e.Name != "" {
				snap.Switches[i].DBName = e.Name
				break
			}
		}
	}

	owner := map[string]string{} // switch MAC → host
	byName := map[string]string{}
	byHost := map[string]bool{}
	for _, sw := range switches {
		for _, m := range sw.MACs {
			owner[m] = sw.Host
		}
		if sw.Name != "" && uniqueName(switches, sw.Name) {
			byName[strings.ToLower(sw.Name)] = sw.Host
		}
		byHost[sw.Host] = true
	}

	// Which switch, if any, a neighbor is.
	neighborSwitch := func(n Neighbor) string {
		if m, ok := NormalizeMAC(n.ChassisID); ok && owner[m] != "" {
			return owner[m]
		}
		if m, ok := NormalizeMAC(n.PortID); ok && owner[m] != "" {
			return owner[m]
		}
		if byHost[n.MgmtIP] {
			return n.MgmtIP
		}
		if h := byName[strings.ToLower(n.SystemName)]; h != "" {
			return h
		}
		return ""
	}

	// Switch-to-switch ports.
	type portKey struct{ sw, port string }
	uplink := map[portKey]bool{}
	linked := map[string]bool{}
	addLink := func(l Link) {
		a := l.FromSwitch + " " + l.FromPort
		b := l.ToSwitch + " " + l.ToPort
		if l.ToSwitch == "" {
			b = "unread " + l.ToName
		}
		key := a + "|" + b
		if a > b {
			key = b + "|" + a
		}
		if !linked[key] {
			linked[key] = true
			snap.Links = append(snap.Links, l)
		}
	}
	for _, sw := range switches {
		for _, p := range sw.Ports {
			for _, n := range p.Neighbors {
				if other := neighborSwitch(n); other != "" && other != sw.Host {
					uplink[portKey{sw.Host, p.Name}] = true
					addLink(Link{FromSwitch: sw.Host, FromPort: p.Name, ToSwitch: other, ToPort: farPort(switches, other, sw)})
				} else if other == "" && isBridge(n) {
					uplink[portKey{sw.Host, p.Name}] = true
					name := n.SystemName
					if name == "" {
						name = n.ChassisID
					}
					addLink(Link{FromSwitch: sw.Host, FromPort: p.Name, ToName: name})
				}
			}
			// No LLDP, but other read switches' own MACs are learned here: the port is an uplink, and
			// it links to the nearest of them (directMACNeighbors).
			if uplink[portKey{sw.Host, p.Name}] {
				continue
			}
			var seen []string
			for _, e := range p.MACs {
				if other := owner[e.MAC]; other != "" && other != sw.Host && !contains(seen, other) {
					seen = append(seen, other)
				}
			}
			if len(seen) == 0 {
				continue
			}
			uplink[portKey{sw.Host, p.Name}] = true
			for _, other := range directMACNeighbors(sw.Host, seen, switches) {
				addLink(Link{FromSwitch: sw.Host, FromPort: p.Name, ToSwitch: other, ToPort: farPort(switches, other, sw)})
			}
		}
	}

	// MAC → IP, from every table that has one.
	ip := map[string]string{}
	for _, a := range extraARP {
		ip[a.MAC] = a.IP
	}
	for _, sw := range switches {
		for _, a := range sw.ARP {
			if !strings.HasPrefix(a.IP, "169.254.") || ip[a.MAC] == "" {
				ip[a.MAC] = a.IP
			}
		}
	}

	// Where each device MAC is: its one edge port, or an uplink if that's all it was seen on.
	type place struct {
		sw, port string
		beyond   bool
	}
	where := map[string]place{}
	neighbors := map[string]Neighbor{} // MAC → the LLDP neighbor that names it
	for _, sw := range switches {
		for _, p := range sw.Ports {
			up := uplink[portKey{sw.Host, p.Name}]
			for _, n := range p.Neighbors {
				for _, id := range []string{n.ChassisID, n.PortID} {
					if m, ok := NormalizeMAC(id); ok {
						neighbors[m] = n
						if n.MgmtIP != "" && ip[m] == "" {
							ip[m] = n.MgmtIP
						}
					}
				}
			}
			for _, e := range p.MACs {
				if owner[e.MAC] != "" {
					continue
				}
				cur, seen := where[e.MAC]
				if !up && (!seen || cur.beyond) {
					where[e.MAC] = place{sw.Host, p.Name, false}
				} else if !seen {
					where[e.MAC] = place{sw.Host, p.Name, true}
				}
			}
		}
	}

	// Group each port's MACs into devices.
	onPort := map[portKey][]string{}
	for m, pl := range where {
		k := portKey{pl.sw, pl.port}
		onPort[k] = append(onPort[k], m)
	}
	keys := make([]portKey, 0, len(onPort))
	for k := range onPort {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].sw != keys[j].sw {
			return keys[i].sw < keys[j].sw
		}
		return portLess(keys[i].port, keys[j].port)
	})
	for _, k := range keys {
		macs := onPort[k]
		sort.Strings(macs)
		groups := groupMACs(macs, db, neighbors, self)
		var devs []Device
		for _, g := range groups {
			d := describe(g.macs, g.how, db, oui, neighbors, ip, self)
			d.Switch, d.Port, d.Beyond = k.sw, k.port, where[g.macs[0]].beyond
			devs = append(devs, d)
		}
		// The box itself first, then any VMs bridged through it.
		sort.SliceStable(devs, func(i, j int) bool { return !devs[i].Virtual && devs[j].Virtual })
		physical := 0
		for _, d := range devs {
			if !d.Virtual {
				physical++
			}
		}
		if physical > 1 && !where[macs[0]].beyond {
			for i := range devs {
				devs[i].Note = fmt.Sprintf("%d devices on this port: an unmanaged switch, or one device not yet known to be one", physical)
			}
		}
		snap.Devices = append(snap.Devices, devs...)
	}
	return snap
}

type group struct {
	macs []string
	how  string
}

// groupMACs splits one port's MACs into devices: by the database (same name and jobsite), then
// adjacent MACs from one manufacturer ("likely"), each VM MAC on its own. This PC's own adapters
// are always one device.
func groupMACs(macs []string, db DeviceDB, neighbors map[string]Neighbor, self map[string][]string) []group {
	parent := make([]int, len(macs))
	how := make([]string, len(macs))
	for i := range parent {
		parent[i] = i
		how[i] = "single"
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(i, j int, h string) {
		ri, rj := find(i), find(j)
		if ri == rj {
			return
		}
		parent[rj] = ri
		// The weakest reason wins: a group joined by a guess is a guess.
		if how[ri] == "likely" || h == "likely" || how[rj] == "likely" {
			how[ri] = "likely"
		} else {
			how[ri] = h
		}
	}
	for i := range macs {
		for j := i + 1; j < len(macs); j++ {
			a, b := db[macs[i]], db[macs[j]]
			_, si := self[macs[i]]
			_, sj := self[macs[j]]
			if si && sj {
				union(i, j, "self")
			} else if a.Name != "" && a.Name == b.Name && a.Jobsite == b.Jobsite {
				union(i, j, "database")
			}
		}
	}
	for i := range macs {
		for j := i + 1; j < len(macs); j++ {
			if find(i) == find(j) || isVirtual(macs[i]) || isVirtual(macs[j]) {
				continue
			}
			if macs[i][:8] == macs[j][:8] && macGap(macs[i], macs[j]) <= likelyGap {
				union(i, j, "likely")
			}
		}
	}
	byRoot := map[int]*group{}
	var order []int
	for i, m := range macs {
		r := find(i)
		g := byRoot[r]
		if g == nil {
			g = &group{how: how[r]}
			byRoot[r] = g
			order = append(order, r)
		}
		g.macs = append(g.macs, m)
	}
	var out []group
	for _, r := range order {
		g := *byRoot[r]
		g.how = how[r]
		if len(g.macs) == 1 {
			g.how = "single"
			if _, ok := neighbors[g.macs[0]]; ok {
				g.how = "lldp"
			}
		}
		out = append(out, g)
	}
	return out
}

// describe names a device from the database, its LLDP neighbor entry, or its manufacturer.
func describe(macs []string, how string, db DeviceDB, oui OUI, neighbors map[string]Neighbor, ip map[string]string, self map[string][]string) Device {
	d := Device{Grouping: how, Manufacturer: oui.Lookup(macs[0])}
	var names []string
	for _, m := range macs {
		e := db[m]
		in := Interface{MAC: m, IP: ip[m]}
		if ips, ok := self[m]; ok {
			d.Self = true
			if in.IP == "" && len(ips) > 0 {
				in.IP = ips[0]
			}
		}
		if e.Name != "" {
			in.Name, in.Source = e.Name, "database"
			names = append(names, e.Name)
		}
		if d.Model == "" {
			d.Model = e.Model
		}
		if d.Jobsite == "" {
			d.Jobsite = e.Jobsite
		}
		if e.Interface != "" {
			in.Name = e.Interface
		}
		d.Interfaces = append(d.Interfaces, in)
		if v := virtualOUI[strings.ReplaceAll(m, ":", "")[:6]]; v != "" {
			d.Virtual = true
			d.Manufacturer = v + " virtual machine"
		}
	}
	switch {
	case len(names) > 0:
		d.Name = commonName(names)
	default:
		for _, m := range macs {
			if n, ok := neighbors[m]; ok {
				if n.SystemName != "" {
					d.Name = n.SystemName
				} else if _, isMAC := NormalizeMAC(n.ChassisID); !isMAC {
					d.Name = n.ChassisID
				}
				if d.Name != "" {
					for i := range d.Interfaces {
						if d.Interfaces[i].MAC == m && d.Interfaces[i].Name == "" {
							d.Interfaces[i].Source = "lldp"
						}
					}
					break
				}
			}
		}
	}
	if d.Self {
		// The PC the scan ran from. Whatever else named it (its LLDP host name, a database entry)
		// stays visible on the second line.
		if d.Name != "" && d.Model == "" {
			d.Model = d.Name
		}
		d.Name = "Your Computer"
	}
	if d.Name == "" && d.Virtual {
		d.Name = fmt.Sprintf("%s VM %s", virtualOUI[strings.ReplaceAll(macs[0], ":", "")[:6]], macs[0][9:])
	}
	if d.Name == "" {
		maker := d.Manufacturer
		if maker == "" {
			maker = "Unknown"
		}
		d.Name = fmt.Sprintf("%s %s", shortMaker(maker), macs[0][9:])
	}
	return d
}

// commonName is the shared start of a device's interface names ("Rack-Tesira" from
// "Rack-Tesira" and "Rack-Tesira-Dante"), or the first name when they share too little.
func commonName(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	p := names[0]
	for _, n := range names[1:] {
		for !strings.HasPrefix(n, p) {
			p = p[:len(p)-1]
		}
	}
	p = strings.TrimRight(p, "-_ .")
	if len(p) < 3 {
		return names[0]
	}
	return p
}

func shortMaker(m string) string {
	f := strings.Fields(strings.NewReplacer(",", " ", ".", " ").Replace(m))
	if len(f) == 0 {
		return m
	}
	return strings.Title(strings.ToLower(f[0]))
}

func isVirtual(mac string) bool { return virtualOUI[strings.ReplaceAll(mac, ":", "")[:6]] != "" }

func isBridge(n Neighbor) bool {
	c := strings.ToLower(n.CapsEnabled)
	return strings.Contains(c, "bridge") || strings.Contains(c, "router")
}

// macGap is the distance between the last three bytes of two MACs.
func macGap(a, b string) int {
	x, _ := strconv.ParseInt(strings.ReplaceAll(a[9:], ":", ""), 16, 64)
	y, _ := strconv.ParseInt(strings.ReplaceAll(b[9:], ":", ""), 16, 64)
	if x > y {
		return int(x - y)
	}
	return int(y - x)
}

// farPort finds the port on switch host whose neighbor is sw.
func farPort(switches []Switch, host string, sw Switch) string {
	mine := map[string]bool{}
	for _, m := range sw.MACs {
		mine[m] = true
	}
	byName := sw.Name != "" && uniqueName(switches, sw.Name)
	for _, o := range switches {
		if o.Host != host {
			continue
		}
		// The neighbor that is sw by its MAC or address, then by a name no other read switch has,
		// then the port where sw's own MAC is learned.
		for _, p := range o.Ports {
			for _, n := range p.Neighbors {
				if m, ok := NormalizeMAC(n.ChassisID); ok && mine[m] || n.MgmtIP == sw.Host {
					return p.Name
				}
			}
		}
		for _, p := range o.Ports {
			for _, n := range p.Neighbors {
				if byName && strings.EqualFold(n.SystemName, sw.Name) {
					return p.Name
				}
			}
		}
		for _, p := range o.Ports {
			for _, e := range p.MACs {
				if mine[e.MAC] {
					return p.Name
				}
			}
		}
	}
	return ""
}

// uniqueName reports whether no other read switch has this system name. Switches often keep their
// model as their name (two TL-SG3428X both answer "TL-SG3428X"), and a shared name can't tell them apart.
func uniqueName(switches []Switch, name string) bool {
	n := 0
	for _, s := range switches {
		if strings.EqualFold(s.Name, name) {
			n++
		}
	}
	return n == 1
}

// portLess orders "0/2" before "0/10".
func portLess(a, b string) bool {
	na, nb := portNumbers(a), portNumbers(b)
	for i := 0; i < len(na) && i < len(nb); i++ {
		if na[i] != nb[i] {
			return na[i] < nb[i]
		}
	}
	if len(na) != len(nb) {
		return len(na) < len(nb)
	}
	return a < b
}

func portNumbers(s string) []int {
	var n []int
	cur, in := 0, false
	for _, c := range s {
		if c >= '0' && c <= '9' {
			cur, in = cur*10+int(c-'0'), true
		} else if in {
			n, cur, in = append(n, cur), 0, false
		}
	}
	if in {
		n = append(n, cur)
	}
	return n
}

// directMACNeighbors picks, from the read switches whose MACs are learned on one of from's ports, the
// ones wired straight to it. A switch's MAC is learned on every port that leads toward it, so a port also
// sees the switches beyond its neighbor. B is not direct when some other seen switch C learns from and B
// on different ports of its own: C then sits between them. A switch hanging off beyond B learns from and B
// on the same port, so it doesn't count against B. Several survive only when the port leads to a shared
// segment (an unmanaged switch with read switches on it).
func directMACNeighbors(from string, seen []string, switches []Switch) []string {
	byHost := map[string]Switch{}
	for _, s := range switches {
		byHost[s.Host] = s
	}
	// portToward is the port on switch c where any of switch h's own MACs is learned.
	portToward := func(c, h string) string {
		for _, p := range byHost[c].Ports {
			for _, e := range p.MACs {
				if contains(byHost[h].MACs, e.MAC) {
					return p.Name
				}
			}
		}
		return ""
	}
	var direct []string
	for _, b := range seen {
		between := false
		for _, c := range seen {
			if c == b {
				continue
			}
			ta, tb := portToward(c, from), portToward(c, b)
			if ta != "" && tb != "" && ta != tb {
				between = true
				break
			}
		}
		if !between {
			direct = append(direct, b)
		}
	}
	return direct
}
