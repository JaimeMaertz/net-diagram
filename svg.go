package netdiagram

import (
	"fmt"
	"html"
	"strings"
)

// Drawing sizes, in pixels.
const (
	portW     = 26 // a port square's pitch on the switch box
	portSq    = 18
	colW      = 190 // a device column
	devW      = 176
	lineH     = 14
	switchH   = 64
	rowGap    = 70 // between a switch's devices and the next switch
	marginX   = 30
	marginTop = 30
	laneGap   = 8 // between two switch-to-switch links' runs in the left gutter
)

// readTrunk is a link between two read switches. It runs from the upper switch's port along that
// switch's wire heights to a lane in the gutter left of every column, down the gutter, and across
// the gap above the lower switch into the top of that switch at its port.
type readTrunk struct {
	upHost, upPort, downHost, downPort string
	lane                               int
	startX, startY, runY               float64
}

// SVG draws a snapshot: each switch as a box with its ports, the devices on each port in a
// column below it, and switch-to-switch links between ports.
func SVG(s Snapshot) string {
	var b strings.Builder
	type pos struct{ x, y float64 }
	portAt := map[string]pos{} // "host port" → bottom centre of the port square
	y := float64(marginTop)
	width := 0.0
	var body strings.Builder

	// Links whose two ends were both read, each with its own lane in the left gutter.
	order := map[string]int{}
	for i, sw := range s.Switches {
		order[sw.Host] = i
	}
	var trunks []*readTrunk
	readLink := map[string]bool{} // "host port" at either end of a readTrunk
	trunkFrom := map[string]*readTrunk{}
	for _, l := range s.Links {
		a, aok := order[l.FromSwitch]
		b, bok := order[l.ToSwitch]
		if l.ToSwitch == "" || !aok || !bok || a == b || l.FromPort == "" || l.ToPort == "" {
			continue
		}
		t := &readTrunk{upHost: l.FromSwitch, upPort: l.FromPort, downHost: l.ToSwitch, downPort: l.ToPort, lane: len(trunks)}
		if b < a {
			t.upHost, t.upPort, t.downHost, t.downPort = l.ToSwitch, l.ToPort, l.FromSwitch, l.FromPort
		}
		trunks = append(trunks, t)
		readLink[l.FromSwitch+" "+l.FromPort] = true
		readLink[l.ToSwitch+" "+l.ToPort] = true
		trunkFrom[t.upHost+" "+t.upPort] = t
	}
	laneX := func(lane int) float64 { return 12 + float64(lane)*laneGap }
	left := float64(marginX + len(trunks)*laneGap)
	switchTop := map[string]float64{}

	for _, sw := range s.Switches {
		devs := map[string][]Device{}
		var used []string
		for _, d := range s.Devices {
			if d.Switch != sw.Host {
				continue
			}
			if len(devs[d.Port]) == 0 {
				used = append(used, d.Port)
			}
			devs[d.Port] = append(devs[d.Port], d)
		}
		linkPort := map[string]bool{}
		unreadAt := map[string]string{} // port → the unread switch beyond it
		for _, l := range s.Links {
			if l.FromSwitch == sw.Host && l.ToSwitch == "" {
				unreadAt[l.FromPort] = l.ToName
			}
			for _, end := range [][2]string{{l.FromSwitch, l.FromPort}, {l.ToSwitch, l.ToPort}} {
				if end[0] == sw.Host && end[1] != "" {
					linkPort[end[1]] = true
					// A port to another read switch has no column of its own unless devices were
					// placed on it; its trunk is drawn after the switches.
					if len(devs[end[1]]) == 0 && !contains(used, end[1]) && !readLink[sw.Host+" "+end[1]] {
						used = append(used, end[1])
					}
				}
			}
		}
		// A port that's up with nothing learned on it: a device that hasn't sent anything lately.
		for _, p := range sw.Ports {
			if p.Up && len(devs[p.Name]) == 0 && !contains(used, p.Name) && !readLink[sw.Host+" "+p.Name] {
				used = append(used, p.Name)
			}
		}
		sortPorts(used)

		boxW := float64(len(sw.Ports)*portW + 40)
		if w := float64(len(used) * colW); w > boxW {
			boxW = w
		}
		x0 := left
		switchTop[sw.Host] = y
		title := sw.Title()
		fmt.Fprintf(&body, `<rect x="%.0f" y="%.0f" width="%.0f" height="%d" rx="6" class="switch"/>`+"\n", x0, y, boxW, switchH)
		fmt.Fprintf(&body, `<text x="%.0f" y="%.0f" class="swname">%s</text>`+"\n", x0+10, y+18, esc(title))
		fmt.Fprintf(&body, `<text x="%.0f" y="%.0f" class="swsub">%s · %s · %s</text>`+"\n", x0+10, y+32, esc(sw.Vendor+" "+sw.Model), esc(sw.Host), esc("fw "+sw.Firmware))

		// Ports along the bottom edge.
		px := x0 + 20
		for _, p := range sw.Ports {
			cls := "port down"
			if p.Up {
				cls = "port up"
			}
			if readLink[sw.Host+" "+p.Name] {
				cls += " link"
			}
			py := y + switchH - portSq - 4
			fmt.Fprintf(&body, `<rect x="%.0f" y="%.0f" width="%d" height="%d" class="%s"><title>%s</title></rect>`+"\n",
				px, py, portSq, portSq, cls, esc(portTitle(p)))
			label := shortPort(p.Name)
			size := ""
			if len(label) > 2 {
				size = ` style="font-size:7px"`
			}
			fmt.Fprintf(&body, `<text x="%.0f" y="%.0f" class="pnum"%s>%s</text>`+"\n", px+portSq/2, py+12, size, esc(label))
			portAt[sw.Host+" "+p.Name] = pos{px + portSq/2, y + switchH}
			px += portW
		}

		// Device columns, and a height for each wire's horizontal run that keeps it off the others.
		// The trunks that start here run to the gutter at these heights too, so they keep off the
		// device wires: they are entries after the columns in froms / tos.
		var froms, tos []float64
		for i, port := range used {
			froms = append(froms, portAt[sw.Host+" "+port].x)
			tos = append(tos, x0+float64(i*colW)+colW/2)
		}
		var starting []*readTrunk
		for _, p := range sw.Ports {
			if t, ok := trunkFrom[sw.Host+" "+p.Name]; ok {
				starting = append(starting, t)
				froms = append(froms, portAt[sw.Host+" "+p.Name].x)
				tos = append(tos, laneX(t.lane))
			}
		}
		levels, nLevels := wireLevels(froms, tos)
		firstRun := y + switchH + 14
		colTop := firstRun + float64(nLevels)*wireGap + 12
		for j, t := range starting {
			p := portAt[sw.Host+" "+t.upPort]
			t.startX, t.startY, t.runY = p.x, p.y, firstRun+float64(levels[len(used)+j])*wireGap
		}
		maxBottom := colTop
		for i, port := range used {
			cx := x0 + float64(i*colW) + (colW-devW)/2
			p := portAt[sw.Host+" "+port]
			mid := firstRun + float64(levels[i])*wireGap
			wire := "wire"
			if _, ok := unreadAt[port]; ok {
				wire = "trunk"
			}
			fmt.Fprintf(&body, `<polyline points="%.0f,%.0f %.0f,%.0f %.0f,%.0f %.0f,%.0f" class="%s"/>`+"\n",
				p.x, p.y, p.x, mid, cx+devW/2, mid, cx+devW/2, colTop, wire)
			label := port
			for _, sp := range sw.Ports {
				if sp.Name == port && sp.PoEmW > 0 {
					label += fmt.Sprintf(" · PoE %.1f W", float64(sp.PoEmW)/1000)
				}
			}
			dy := colTop
			// A switch that wasn't read heads its port's column; what was seen beyond it hangs below.
			if name, ok := unreadAt[port]; ok {
				fmt.Fprintf(&body, `<rect x="%.0f" y="%.0f" width="%d" height="32" rx="4" class="unread"/>`+"\n", cx, dy, devW)
				fmt.Fprintf(&body, `<text x="%.0f" y="%.0f" class="dname">%s</text>`+"\n", cx+6, dy+14, esc(clip(name, 26)))
				fmt.Fprintf(&body, `<text x="%.0f" y="%.0f" class="small">switch, not read</text>`+"\n", cx+6, dy+27)
				dy += 32 + 10
			}
			for _, d := range devs[port] {
				h := deviceBox(&body, d, cx, dy)
				dy += h + 10
			}
			if len(devs[port]) == 0 && !linkPort[port] {
				fmt.Fprintf(&body, `<rect x="%.0f" y="%.0f" width="%d" height="34" rx="4" class="dev quiet"/>`+"\n", cx, dy, devW)
				fmt.Fprintf(&body, `<text x="%.0f" y="%.0f" class="small">link up, nothing learned</text>`+"\n", cx+6, dy+14)
				fmt.Fprintf(&body, `<text x="%.0f" y="%.0f" class="small">(quiet, or just restarted)</text>`+"\n", cx+6, dy+27)
				dy += 34 + 10
			}
			// The port label sits under the column, clear of the wires above it.
			fmt.Fprintf(&body, `<text x="%.0f" y="%.0f" class="small" text-anchor="middle">%s</text>`+"\n", cx+devW/2, dy+2, esc(label))
			dy += 16
			if dy > maxBottom {
				maxBottom = dy
			}
		}
		if w := x0 + boxW + marginX; w > width {
			width = w
		}
		y = maxBottom + rowGap
	}

	// Links between two read switches: down from the upper port to its run, out to the gutter, down
	// the gutter, and across the gap above the lower switch into its top at the port. (A switch
	// that wasn't read is drawn at the top of its port's column instead.)
	for _, t := range trunks {
		bpos, ok := portAt[t.downHost+" "+t.downPort]
		if !ok || t.startY == 0 {
			continue
		}
		top := switchTop[t.downHost]
		entry := top - 14 - float64(t.lane)*laneGap
		lx := laneX(t.lane)
		fmt.Fprintf(&body, `<polyline points="%.0f,%.0f %.0f,%.0f %.0f,%.0f %.0f,%.0f %.0f,%.0f %.0f,%.0f" class="trunk"/>`+"\n",
			t.startX, t.startY, t.startX, t.runY, lx, t.runY, lx, entry, bpos.x, entry, bpos.x, top)
		// The label sits left of the lower port; when the port is near the left edge it would run off
		// the sheet (or over the gutter), so it goes right of the port instead.
		label := t.upPort + " ↔ " + t.downPort
		lblX, anchor := bpos.x-6, "end"
		if lblX-labelWidth(label) < lx+4 {
			lblX, anchor = bpos.x+6, "start"
		}
		fmt.Fprintf(&body, `<text x="%.0f" y="%.0f" class="small" text-anchor="%s">%s</text>`+"\n",
			lblX, entry-4, anchor, esc(label))
	}

	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" font-family="Segoe UI, Arial, sans-serif">`+"\n", width, y, width, y)
	b.WriteString(`<style>
.switch{fill:#1f2937;stroke:#111827}
.swname{fill:#fff;font-size:14px;font-weight:600}
.swsub{font-size:10px;fill:#d1d5db}
.small{font-size:10px;fill:#4b5563}
.port{stroke:#111827;stroke-width:1}
.up{fill:#22c55e}.down{fill:#6b7280}
.link{stroke:#f59e0b;stroke-width:2.5}
.pnum{font-size:9px;fill:#000;text-anchor:middle}
.wire{fill:none;stroke:#2563eb;stroke-width:1.5}
.trunk{fill:none;stroke:#f59e0b;stroke-width:3}
.dev{fill:#fff;stroke:#374151}
.likely{stroke-dasharray:5 3}
.virtual{fill:#f3f4f6;stroke-dasharray:2 2}
.self{fill:#dbeafe;stroke:#2563eb;stroke-width:1.5}
.beyond{fill:#fef3c7}
.quiet{fill:#f9fafb;stroke-dasharray:4 3}
.unread{fill:#fef3c7;stroke:#b45309}
.dname{font-size:12px;font-weight:600;fill:#111827}
.mono{font-family:Consolas, monospace;font-size:10px;fill:#111827}
.note{font-size:9px;fill:#b45309}
</style>
<rect width="100%" height="100%" fill="#f9fafb"/>
`)
	b.WriteString(body.String())
	b.WriteString("</svg>\n")
	return b.String()
}

// wireGap is the vertical distance between two wires' horizontal runs.
const wireGap = 8

// wireLevels picks a height (0 = nearest the switch) for each wire's horizontal run. Wire i drops
// from the switch at from[i], runs across, and drops to its device at to[i]. For two wires a and b
// whose runs overlap:
//   - a's drop from the switch inside b's run means b's run must be below a's;
//   - a's drop to its device inside b's run means b's run must be above a's;
//   - otherwise they only need different heights.
//
// Heights are the longest path through those "below" constraints. A cycle (a crossing that can't
// be avoided) is broken by putting the remaining wires on fresh heights. Returns the heights and
// how many are used.
func wireLevels(from, to []float64) ([]int, int) {
	n := len(from)
	lo := func(i int) float64 { return min(from[i], to[i]) }
	hi := func(i int) float64 { return max(from[i], to[i]) }
	in := func(x float64, i int) bool { return x > lo(i)-1 && x < hi(i)+1 }
	below := make([][]bool, n) // below[a][b]: b's run must be below a's
	for i := range below {
		below[i] = make([]bool, n)
	}
	for a := 0; a < n; a++ {
		for b := 0; b < n; b++ {
			if a == b {
				continue
			}
			if in(from[a], b) {
				below[a][b] = true
			}
			if in(to[a], b) {
				below[b][a] = true
			}
		}
	}
	for a := 0; a < n; a++ {
		for b := a + 1; b < n; b++ {
			overlap := lo(a) < hi(b)+1 && lo(b) < hi(a)+1
			if overlap && !below[a][b] && !below[b][a] {
				below[a][b] = true
			}
		}
	}
	// Longest path, Kahn's order; whatever is left in a cycle goes on fresh heights.
	level := make([]int, n)
	indeg := make([]int, n)
	for a := 0; a < n; a++ {
		for b := 0; b < n; b++ {
			if below[a][b] {
				indeg[b]++
			}
		}
	}
	done := make([]bool, n)
	var queue []int
	for i := 0; i < n; i++ {
		if indeg[i] == 0 {
			queue = append(queue, i)
		}
	}
	for len(queue) > 0 {
		a := queue[0]
		queue = queue[1:]
		done[a] = true
		for b := 0; b < n; b++ {
			if below[a][b] && !done[b] {
				if level[a]+1 > level[b] {
					level[b] = level[a] + 1
				}
				if indeg[b]--; indeg[b] == 0 {
					queue = append(queue, b)
				}
			}
		}
	}
	top := 0
	for i := 0; i < n; i++ {
		if done[i] && level[i]+1 > top {
			top = level[i] + 1
		}
	}
	for i := 0; i < n; i++ {
		if !done[i] {
			level[i] = top
			top++
		}
	}
	return level, top
}

// deviceBox draws one device and returns its height.
func deviceBox(b *strings.Builder, d Device, x, y float64) float64 {
	lines := 2 + len(d.Interfaces)
	if d.Grouping == "likely" {
		lines++
	}
	if d.Note != "" {
		lines += 2
	}
	h := float64(10 + lines*lineH)
	cls := "dev"
	switch {
	case d.Self:
		cls += " self"
	case d.Virtual:
		cls += " virtual"
	case d.Beyond:
		cls += " beyond"
	}
	if d.Grouping == "likely" {
		cls += " likely"
	}
	fmt.Fprintf(b, `<rect x="%.0f" y="%.0f" width="%d" height="%.0f" rx="4" class="%s"/>`+"\n", x, y, devW, h, cls)
	ty := y + 15
	fmt.Fprintf(b, `<text x="%.0f" y="%.0f" class="dname">%s</text>`+"\n", x+6, ty, esc(clip(d.Name, 26)))
	ty += lineH
	sub := d.Model
	if sub == "" {
		sub = d.Manufacturer
	}
	if d.Beyond {
		sub = strings.TrimSuffix("beyond an unread switch · "+sub, " · ")
	}
	fmt.Fprintf(b, `<text x="%.0f" y="%.0f" class="small">%s</text>`+"\n", x+6, ty, esc(clip(sub, 34)))
	for _, in := range d.Interfaces {
		ty += lineH
		t := in.MAC
		if in.IP != "" {
			t += "  " + in.IP
		}
		fmt.Fprintf(b, `<text x="%.0f" y="%.0f" class="mono">%s<title>%s</title></text>`+"\n", x+6, ty, esc(t), esc(in.Name))
	}
	if d.Grouping == "likely" {
		ty += lineH
		fmt.Fprintf(b, `<text x="%.0f" y="%.0f" class="note">likely one device (adjacent MACs)</text>`+"\n", x+6, ty)
	}
	if d.Note != "" {
		ty += lineH
		fmt.Fprintf(b, `<text x="%.0f" y="%.0f" class="note">%s</text>`+"\n", x+6, ty, esc(clip(d.Note, 40)))
		ty += lineH
		if len(d.Note) > 40 {
			fmt.Fprintf(b, `<text x="%.0f" y="%.0f" class="note">%s</text>`+"\n", x+6, ty, esc(clip(d.Note[40:], 40)))
		}
	}
	return h
}

func portTitle(p Port) string {
	t := p.Name
	if p.Description != "" {
		t += " " + p.Description
	}
	if p.Up {
		t += " · up " + p.Speed
	} else {
		t += " · down"
	}
	if p.PoEmW > 0 {
		t += fmt.Sprintf(" · PoE %.1f W", float64(p.PoEmW)/1000)
	}
	return t
}

// shortPort is the last number of a port name ("0/12" → "12").
// The type is kept for anything but gigabit ("te1/0/1" → "Te1"), so a 10G port 1 isn't drawn as
// another port 1.
func shortPort(name string) string {
	num := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		num = name[i+1:]
	}
	prefix := strings.ToLower(strings.TrimRight(name, "0123456789/"))
	if prefix == "" || prefix == "gi" || prefix == "gigabitethernet" || len(prefix) < 2 {
		return num
	}
	return strings.ToUpper(prefix[:1]) + prefix[1:2] + num
}

func clip(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func esc(s string) string { return html.EscapeString(s) }

func contains(s []string, x string) bool {
	for _, y := range s {
		if y == x {
			return true
		}
	}
	return false
}

func sortPorts(p []string) {
	for i := 1; i < len(p); i++ {
		for j := i; j > 0 && portLess(p[j], p[j-1]); j-- {
			p[j], p[j-1] = p[j-1], p[j]
		}
	}
}

// labelWidth estimates a .small label's width in pixels (about 6 px a character at its size).
func labelWidth(s string) float64 { return float64(len([]rune(s))) * 6 }
