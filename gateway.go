package netdiagram

import (
	"encoding/binary"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// gatewayType matches a device database type that means the network's way out.
var gatewayType = regexp.MustCompile(`(?i)^\s*(gateway|router|firewall)\s*$`)

// MarkGateways marks the devices that are a gateway: one holding an address this PC routes its
// default traffic through (gateways, from LocalGateways at read time, kept in the snapshot), or one the
// device database types "gateway", "router" or "firewall". Passing nil gateways keeps the snapshot's own,
// so a saved snapshot marks the same devices; it is safe to call again.
func MarkGateways(snap *Snapshot, db DeviceDB, gateways []string) {
	if gateways != nil {
		snap.Gateways = gateways
	}
	gw := map[string]bool{}
	for _, ip := range snap.Gateways {
		gw[ip] = true
	}
	for i := range snap.Devices {
		d := &snap.Devices[i]
		d.Gateway = ""
		for _, in := range d.Interfaces {
			switch {
			case in.IP != "" && gw[in.IP]:
				d.Gateway = "this PC's default gateway" // the address is on the device's own line
			case d.Gateway == "" && gatewayType.MatchString(db[in.MAC].Type):
				d.Gateway = "device database"
			}
		}
	}
}

// LocalGateways returns this PC's IPv4 default gateways: the next hop of each 0.0.0.0/0 route. It reads
// "route print" on Windows and /proc/net/route on Linux, and returns none when it can't.
func LocalGateways() []string {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("route", "print", "-4", "0.0.0.0").Output()
		if err != nil {
			return nil
		}
		return parseRoutePrint(string(out))
	}
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return nil
	}
	return parseProcRoute(string(b))
}

// parseRoutePrint reads the default routes from Windows' "route print" (its Active Routes table:
// destination, netmask, gateway, interface, metric). A route "On-link" has no gateway.
func parseRoutePrint(out string) []string {
	var gws []string
	active := false
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		switch {
		case strings.HasPrefix(line, "Active Routes"):
			active = true
			continue
		case strings.HasPrefix(line, "Persistent Routes"):
			active = false
		}
		f := strings.Fields(line)
		if active && len(f) >= 3 && f[0] == "0.0.0.0" && f[1] == "0.0.0.0" && net.ParseIP(f[2]) != nil && !contains(gws, f[2]) {
			gws = append(gws, f[2])
		}
	}
	return gws
}

// parseProcRoute reads the default routes from Linux's /proc/net/route, whose addresses are hex in
// the kernel's byte order (little-endian on every machine this runs on).
func parseProcRoute(out string) []string {
	var gws []string
	for _, line := range strings.Split(out, "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 3 || f[1] != "00000000" {
			continue
		}
		b, err := hex.DecodeString(f[2])
		if err != nil || len(b) != 4 {
			continue
		}
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, binary.LittleEndian.Uint32(b))
		if s := ip.String(); s != "0.0.0.0" && !contains(gws, s) {
			gws = append(gws, s)
		}
	}
	return gws
}
