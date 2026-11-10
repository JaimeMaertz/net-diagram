package netdiagram

import (
	"regexp"
	"strings"
)

// allowed holds every command netdiagram may send to a switch, as typed. Each one only reads.
// Nothing here enters configuration mode or changes a setting; "terminal length" and its
// equivalents change only the current session's paging.
var allowed = map[string]bool{
	// Netgear M4250 (CLI Command Reference Manual, M4250/M4350, 202-12843-05)
	"show version":                true, // p. 260
	"show sysinfo":                true, // p. 292
	"show interfaces status all":  true, // p. 271
	"show lldp remote-device all": true, // p. 828
	"show isdp neighbors":         true, // p. 863
	"show mac-addr-table all":     true, // p. 280
	"show poe port info all":      true, // p. 1483
	"show arp switch":             true, // p. 258
	"show terminal length":        true, // p. 293
	"show lldp local-device all":  true,

	// Cisco Catalyst 1300 / CBS350 (Cisco Catalyst 1300 Switches Series CLI Guide, 2026-04-28;
	// "show version" is p. 515)
	"show system":                 true, // p. 1453
	"show inventory":              true, // p. 1438
	"show interfaces status":      true, // p. 472
	"show interfaces description": true, // p. 475
	"show lldp neighbors":         true, // p. 892
	"show mac address-table":      true, // p. 157
	"show power inline":           true, // p. 1030
	"show arp":                    true, // p. 605
}

// portArg matches the per-port show commands: "show lldp remote-device detail 0/5" (p. 829) and
// "show port description 0/5" (p. 490), with a slot/port or unit/slot/port argument only.
var portArg = regexp.MustCompile(`^show (lldp remote-device detail|port description) \d+(/\d+){1,2}$` +
	// Cisco: "show lldp neighbors gi1/0/5" (also te, fa, and the long forms)
	`|^show lldp neighbors (?i:gi|te|fa|gigabitethernet|tengigabitethernet|fastethernet)\d+(/\d+){1,2}$`)

// Allowed reports whether cmd may be sent.
func Allowed(cmd string) bool {
	c := strings.Join(strings.Fields(cmd), " ")
	if allowed[c] {
		return true
	}
	return portArg.MatchString(c)
}
