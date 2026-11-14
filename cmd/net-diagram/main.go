// net-diagram reads switches and draws how they and their devices connect.
//
//	net-diagram                       the web interface in the browser (also: net-diagram gui [-no-browser])
//	net-diagram discover -host 172.22.0.4 -cred net-diagram:28:94:01:AF:35:BD -out site.json [-raw dir]
//	net-diagram discover -host 172.22.0.4 -from testdata\m4250 -out site.json    (saved output, any OS)
//	net-diagram draw site.json -o site.svg
//
// -host and -cred (or -from) repeat, one per switch, in the same order. The device database is the
// devices.json beside the pinger on PATH (the database that pinger names devices from), else beside
// this executable or in ../pinger; the manufacturer list is pinger's oui.txt, found the same way.
// -db and -oui override.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/JaimeMaertz/net-diagram"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(s string) error { *l = append(*l, s); return nil }

// version is set at release build time: go build -ldflags "-X main.version=v1.0.0".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		gui(nil) // started with no arguments, as from a double-click
		return
	}
	switch os.Args[1] {
	case "version", "-version", "--version":
		fmt.Println("net-diagram", version)
	case "gui":
		gui(os.Args[2:])
	case "discover":
		discover(os.Args[2:])
	case "draw":
		draw(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  net-diagram                       (the web interface; also: net-diagram gui [-no-browser])
  net-diagram discover [-vendor Netgear|Cisco|TP-Link] -host IP (-cred TARGET | -from DIR) [-host … ] -out snapshot.json [-raw DIR] [-db devices.json] [-oui oui.txt]
  net-diagram draw snapshot.json -o diagram.svg`)
	os.Exit(2)
}

func discover(args []string) {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	var hosts, creds, froms list
	fs.Var(&hosts, "host", "switch address (repeat for each switch)")
	fs.Var(&creds, "cred", "Credential Manager target with the switch's login (Windows)")
	fs.Var(&froms, "from", "folder of saved command output instead of logging in")
	out := fs.String("out", "", "snapshot file to write")
	raw := fs.String("raw", "", "also save each switch's raw output under this folder")
	vendor := fs.String("vendor", "Netgear", "Netgear, Cisco or TP-Link, for logging in (with -from it's read from the output)")
	dbPath := fs.String("db", findPingerFile("devices.json"), "device database")
	ouiPath := fs.String("oui", findPingerFile("oui.txt"), "manufacturer list")
	fs.Parse(args)
	if len(hosts) == 0 || *out == "" || (len(creds) != len(hosts) && len(froms) != len(hosts)) {
		usage()
	}
	db, err := netdiagram.LoadDeviceDB(*dbPath)
	check(err)
	warnMissingOUI(*ouiPath)
	oui, err := netdiagram.LoadOUI(*ouiPath)
	check(err)

	var switches []netdiagram.Switch
	for i, h := range hosts {
		var outputs map[string]string
		v := *vendor
		if len(froms) == len(hosts) {
			outputs, err = netdiagram.LoadOutputs(froms[i])
			if got := netdiagram.VendorOf(outputs); got != "" {
				v = got
			}
		} else {
			outputs, err = collectLive(v, h, creds[i])
		}
		check(err)
		if *raw != "" {
			saveRaw(filepath.Join(*raw, h), outputs)
		}
		sw, err := netdiagram.Parse(v, h, outputs)
		check(err)
		fmt.Printf("%s  %s %s, %d ports, %d MACs learned\n", h, sw.Vendor, sw.Model, len(sw.Ports), countMACs(sw))
		switches = append(switches, sw)
	}
	// Read live, the switches see this PC; saved output may come from another one.
	var self map[string][]string
	if len(froms) != len(hosts) {
		self = netdiagram.LocalMACs()
	}
	snap := netdiagram.Build(switches, db, oui, nil, self)
	if len(froms) != len(hosts) {
		// Live: ask the devices sharing a port whether they're access points (SSH banner, web server).
		if fp := netdiagram.FingerprintAPs(snap); len(fp) > 0 {
			netdiagram.MarkAccessPoints(&snap, db, fp)
		}
		netdiagram.MarkGateways(&snap, db, netdiagram.LocalGateways())
	}
	b, _ := json.MarshalIndent(snap, "", "  ")
	check(os.WriteFile(*out, b, 0o644))
	for _, d := range snap.Devices {
		fmt.Printf("  %-6s %-28s %-9s %s\n", d.Port, d.Name, d.Grouping, macsOf(d))
	}
	fmt.Printf("%d devices, %d switch links → %s (database: %d entries)\n", len(snap.Devices), len(snap.Links), *out, len(db))
}

func draw(args []string) {
	fs := flag.NewFlagSet("draw", flag.ExitOnError)
	out := fs.String("o", "", "SVG file to write")
	fs.Parse(args)
	if fs.NArg() < 1 {
		usage()
	}
	file := fs.Arg(0)
	fs.Parse(fs.Args()[1:]) // flags may also follow the file name
	if *out == "" {
		usage()
	}
	b, err := os.ReadFile(file)
	check(err)
	var snap netdiagram.Snapshot
	check(json.Unmarshal(b, &snap))
	check(os.WriteFile(*out, []byte(netdiagram.SVG(snap)), 0o644))
	fmt.Println("wrote", *out)
}

// findPingerFile looks for a pinger file (pinger.exe, devices.json, oui.txt) in the folder of the
// pinger on PATH first: that is the copy in use, and its devices.json is the database it names
// devices from. Then findBeside. When the file is nowhere, it names the place it belongs: beside
// the pinger on PATH, else beside this executable.
func findPingerFile(name string) string {
	var pathDir string
	if p, err := exec.LookPath("pinger"); err == nil {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		pathDir = filepath.Dir(p)
		cand := filepath.Join(pathDir, name)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	if p := findBeside(name); p != "" {
		return p
	}
	if pathDir != "" {
		return filepath.Join(pathDir, name)
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), name)
	}
	return name
}

// warnMissingOUI says so when the manufacturer list isn't there: the map still draws, with no
// manufacturer names.
func warnMissingOUI(path string) {
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(os.Stderr, "warning: no manufacturer list at %s; manufacturer names will be blank. "+
			"Keep pinger's oui.txt beside pinger (or beside net-diagram), or pass -oui.\n", path)
	}
}

// findBeside looks for pinger's file next to the executable, then in ../pinger, then in the
// source tree's ../pinger when run with go run from a checkout beside pinger, and returns "" when
// none has it.
func findBeside(name string) string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		dirs = append(dirs, d, filepath.Join(d, "..", "pinger"))
	}
	dirs = append(dirs, filepath.Join("..", "pinger"))
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func saveRaw(dir string, outputs map[string]string) {
	check(netdiagram.SaveOutputs(dir, outputs))
}

func countMACs(sw netdiagram.Switch) int {
	n := 0
	for _, p := range sw.Ports {
		n += len(p.MACs)
	}
	return n
}

func macsOf(d netdiagram.Device) string {
	var m []string
	for _, in := range d.Interfaces {
		s := in.MAC
		if in.IP != "" {
			s += " " + in.IP
		}
		m = append(m, s)
	}
	return strings.Join(m, ", ")
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "net-diagram:", err)
		os.Exit(1)
	}
}
