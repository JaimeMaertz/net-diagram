package netdiagram

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// An access point's wireless clients are learned on the switch port the access point is wired to,
// so on the switch they look like more devices on that port. MarkAccessPoints finds the access points
// and draws the rest of their port's devices beneath them.

// apType matches a device database type that means an access point.
var apType = regexp.MustCompile(`(?i)^\s*(ap|wap|wifi|wi-fi|access ?point|wireless( access point)?)\s*$`)

// MarkAccessPoints marks each device that is an access point, and on a port whose only access
// point it is, sets every other device's Via to it (wireless clients, or devices wired to the access
// point's own LAN ports). It also writes each port's note. A device is an access point when an LLDP
// neighbor with its MAC says "WLAN Access Point", when the device database types it as one, or when
// fingerprints (MAC → what was seen, from FingerprintAPs) names it. Running it again with more
// fingerprints recomputes everything, so it's safe to call after Build.
func MarkAccessPoints(snap *Snapshot, db DeviceDB, fingerprints map[string]string) {
	lldpAP := map[string]bool{}
	for _, sw := range snap.Switches {
		for _, p := range sw.Ports {
			for _, n := range p.Neighbors {
				if !strings.Contains(strings.ToLower(n.CapsEnabled), "wlan access point") {
					continue
				}
				for _, id := range []string{n.ChassisID, n.PortID} {
					if m, ok := NormalizeMAC(id); ok {
						lldpAP[m] = true
					}
				}
			}
		}
	}

	type portKey struct{ sw, port string }
	byPort := map[portKey][]int{}
	var order []portKey
	for i := range snap.Devices {
		d := &snap.Devices[i]
		d.AccessPoint, d.Via = "", ""
		for _, in := range d.Interfaces {
			switch {
			case lldpAP[in.MAC]:
				d.AccessPoint = "LLDP"
			case d.AccessPoint == "" && apType.MatchString(db[in.MAC].Type):
				d.AccessPoint = "device database"
			case d.AccessPoint == "" && fingerprints[in.MAC] != "":
				d.AccessPoint = "likely: " + fingerprints[in.MAC]
			}
		}
		k := portKey{d.Switch, d.Port}
		if _, ok := byPort[k]; !ok {
			order = append(order, k)
		}
		byPort[k] = append(byPort[k], i)
	}

	for _, k := range order {
		idx := byPort[k]
		physical, beyond := 0, false
		var aps []int
		for _, i := range idx {
			d := &snap.Devices[i]
			d.Note = ""
			beyond = beyond || d.Beyond
			if !d.Virtual {
				physical++
			}
			if d.AccessPoint != "" {
				aps = append(aps, i)
			}
		}
		switch {
		case beyond:
			// Seen only on an uplink: which switch or access point they hang off is unknown.
		case len(aps) == 1:
			ap := &snap.Devices[aps[0]]
			why := ap.AccessPoint
			if strings.HasPrefix(why, "likely: ") {
				why = "likely (" + strings.TrimPrefix(why, "likely: ") + ")"
			} else {
				why = "from " + why
			}
			if physical > 1 {
				ap.Note = fmt.Sprintf("access point: %d devices through it · %s", physical-1, why)
			} else {
				ap.Note = "access point · " + why
			}
			for _, i := range idx {
				if i != aps[0] {
					snap.Devices[i].Via = ap.Interfaces[0].MAC
					snap.Devices[i].Note = "through " + ap.Name + " (wireless, or wired to it)"
				}
			}
		case len(aps) > 1:
			for _, i := range idx {
				d := &snap.Devices[i]
				if d.AccessPoint != "" {
					d.Note = fmt.Sprintf("access point (%s); %d access points on this port, so which devices go through which is unknown", d.AccessPoint, len(aps))
				} else {
					d.Note = fmt.Sprintf("%d devices and %d access points on this port", physical, len(aps))
				}
			}
		case physical > 1:
			for _, i := range idx {
				snap.Devices[i].Note = fmt.Sprintf("%d devices on this port: an unmanaged switch, or one device not yet known to be one", physical)
			}
		}
		// The access point heads its port's devices, so the ones through it can hang beneath it.
		if len(aps) == 1 {
			sub := make([]Device, len(idx))
			for j, i := range idx {
				sub[j] = snap.Devices[i]
			}
			sort.SliceStable(sub, func(a, b int) bool { return sub[a].AccessPoint != "" && sub[b].AccessPoint == "" })
			for j, i := range idx {
				snap.Devices[i] = sub[j]
			}
		}
	}
}

// apSignature is what an access point's own SSH or web server shows: a maker (from its MAC) and a
// pattern in its SSH banner or its web server's Server header. Matched only against devices that share
// a switch port with other devices, so a single device answering the same way isn't taken for one.
type apSignature struct {
	maker, ssh, server *regexp.Regexp
	label              string
}

var apSignatures = []apSignature{
	// UniFi access points run dropbear for SSH (Ubiquiti's switches and gateways share the maker, so this
	// stays a "likely").
	{maker: regexp.MustCompile(`(?i)ubiquiti`), ssh: regexp.MustCompile(`(?i)dropbear`), label: "Ubiquiti, SSH dropbear"},
	// TP-Link access points answer with TP-Link's own web server; its switches say "Web Switch" and its
	// smart plugs "SHIP 2.0".
	{maker: regexp.MustCompile(`(?i)tp-?link`), server: regexp.MustCompile(`(?i)^TP-LINK HTTPD`), label: "TP-Link, web server TP-LINK HTTPD"},
}

// FingerprintAPs probes the devices that share a port with other devices (the ports an access point
// would explain) and returns MAC → what matched for each that looks like an access point. It reads the
// SSH banner and the web server's Server header, which change nothing on the device. Devices already
// known to be access points, or without an IP, are skipped.
func FingerprintAPs(snap Snapshot) map[string]string {
	count := map[string]int{}
	for _, d := range snap.Devices {
		if !d.Virtual && !d.Beyond {
			count[d.Switch+" "+d.Port]++
		}
	}
	found := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	for _, d := range snap.Devices {
		if d.Virtual || d.Beyond || d.AccessPoint != "" || count[d.Switch+" "+d.Port] < 2 {
			continue
		}
		var sigs []apSignature
		for _, s := range apSignatures {
			if s.maker.MatchString(d.Manufacturer) {
				sigs = append(sigs, s)
			}
		}
		if len(sigs) == 0 {
			continue
		}
		for _, in := range d.Interfaces {
			if in.IP == "" {
				continue
			}
			wg.Add(1)
			go func(mac, ip string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if label := matchAP(ip, sigs); label != "" {
					mu.Lock()
					found[mac] = label
					mu.Unlock()
				}
			}(in.MAC, in.IP)
		}
	}
	wg.Wait()
	return found
}

// The two reads a fingerprint takes; tests stand in for the network here.
var (
	readBanner = func(ip string) string { return SSHBanner(ip, 2*time.Second) }
	readServer = webServer
)

func matchAP(ip string, sigs []apSignature) string {
	var banner, server string
	for _, s := range sigs {
		if s.ssh != nil && banner == "" {
			banner = readBanner(ip)
		}
		if s.server != nil && server == "" {
			server = readServer(ip)
		}
		if s.ssh != nil && s.ssh.MatchString(banner) || s.server != nil && s.server.MatchString(server) {
			return s.label
		}
	}
	return ""
}

// webServer returns the Server header of the first of http://ip/ and https://ip/ that answers.
func webServer(ip string) string {
	for _, u := range []string{"http://" + ip + "/", "https://" + ip + "/"} {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		c := *webClient
		c.Timeout = WebTimeout
		resp, err := c.Do(req)
		if err != nil {
			continue
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if s := resp.Header.Get("Server"); s != "" {
			return s
		}
	}
	return ""
}
