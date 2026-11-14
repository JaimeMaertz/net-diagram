package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JaimeMaertz/net-diagram"
)

//go:embed web/index.html
var web embed.FS

// gui serves the web interface on 127.0.0.1 and opens it in the browser. Every API call must
// carry the token made at start, so no other web page can reach it.
func gui(args []string) {
	tokenBytes := make([]byte, 16)
	rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)

	g := &guiServer{
		token:   token,
		dbPath:  findPingerFile("devices.json"),
		ouiPath: findPingerFile("oui.txt"),
		pinger:  findPinger(),
		dir:     defaultSnapshotDir(),
	}
	warnMissingOUI(g.ouiPath)
	mux := http.NewServeMux()
	mux.HandleFunc("/", g.index)
	mux.HandleFunc("/api/init", g.api(g.init))
	mux.HandleFunc("/api/scan", g.api(g.scan))
	mux.HandleFunc("/api/login", g.api(g.login))
	mux.HandleFunc("/api/discover", g.api(g.discover))
	mux.HandleFunc("/api/snapshots", g.api(g.snapshots))
	mux.HandleFunc("/api/snapshot", g.api(g.snapshot))

	ln, err := net.Listen("tcp", "127.0.0.1:8765")
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		check(err)
	}
	url := fmt.Sprintf("http://%s/?t=%s", ln.Addr(), token)
	fmt.Println("net-diagram is running at", url)
	fmt.Println("Close this window to stop it.")
	if !contains(args, "-no-browser") {
		openBrowser(url)
	}
	check(http.Serve(ln, mux))
}

type guiServer struct {
	token, dbPath, ouiPath, pinger, dir string

	mu   sync.Mutex
	last netdiagram.Sweep // the last scan, for its MAC → IP pairs
}

func (g *guiServer) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, _ := web.ReadFile("web/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

// api wraps a handler: checks the token, decodes a JSON body into a map, encodes the result.
func (g *guiServer) api(h func(req map[string]any) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != g.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		req := map[string]any{}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		for k, v := range r.URL.Query() {
			req[k] = v[0]
		}
		res, err := h(req)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(res)
	}
}

func (g *guiServer) init(map[string]any) (any, error) {
	db, err := netdiagram.LoadDeviceDB(g.dbPath)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"subnets":      netdiagram.LocalSubnets(),
		"dir":          g.dir,
		"db":           g.dbPath,
		"db_entries":   len(db),
		"pinger":       g.pinger,
		"pinger_found": fileExists(g.pinger),
	}, nil
}

func (g *guiServer) scan(req map[string]any) (any, error) {
	var subnets []string
	for _, s := range strings.FieldsFunc(str(req["subnets"]), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		subnets = append(subnets, s)
	}
	if len(subnets) == 0 {
		return nil, fmt.Errorf("no subnet to scan")
	}
	if !fileExists(g.pinger) {
		return nil, fmt.Errorf("pinger.exe not found (looked for %s)", g.pinger)
	}
	db, err := netdiagram.LoadDeviceDB(g.dbPath)
	if err != nil {
		return nil, err
	}
	sw, err := netdiagram.RunSweep(g.pinger, subnets)
	if err != nil {
		return nil, err
	}
	netdiagram.FindSwitches(sw.Hosts, db, func(mac string) bool {
		_, _, err := netdiagram.ReadCredential(netdiagram.LoginTarget(mac))
		return err == nil
	})
	g.mu.Lock()
	g.last = sw
	g.mu.Unlock()
	return sw, nil
}

func (g *guiServer) login(req map[string]any) (any, error) {
	mac, ok := netdiagram.NormalizeMAC(str(req["mac"]))
	if !ok {
		return nil, fmt.Errorf("no MAC for this switch, so the login has nowhere to be saved")
	}
	user := str(req["user"])
	if user == "" {
		return nil, fmt.Errorf("a user name is needed")
	}
	if err := netdiagram.WriteCredential(netdiagram.CredTarget(mac), user, str(req["password"])); err != nil {
		return nil, err
	}
	return map[string]any{"saved": netdiagram.CredTarget(mac)}, nil
}

type switchResult struct {
	IP    string `json:"ip"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Model string `json:"model,omitempty"`
}

func (g *guiServer) discover(req map[string]any) (any, error) {
	db, err := netdiagram.LoadDeviceDB(g.dbPath)
	if err != nil {
		return nil, err
	}
	oui, err := netdiagram.LoadOUI(g.ouiPath)
	if err != nil {
		return nil, err
	}
	dir := str(req["dir"])
	if dir == "" {
		dir = g.dir
	}
	list, _ := req["switches"].([]any)
	if len(list) == 0 {
		return nil, fmt.Errorf("no switch chosen")
	}
	var switches []netdiagram.Switch
	var results []switchResult
	for _, item := range list {
		m, _ := item.(map[string]any)
		ip, mac, vendor := str(m["ip"]), str(m["mac"]), str(m["vendor"])
		res := switchResult{IP: ip}
		outputs, err := collectLive(vendor, ip, netdiagram.LoginTarget(mac))
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		// Every read's raw output is kept beside the snapshots: the record of what the switch
		// said, and the test fixtures for a vendor whose parser isn't written yet.
		rawDir := filepath.Join(dir, "raw", ip+"_"+time.Now().Format("2006-01-02_150405"))
		if err := netdiagram.SaveOutputs(rawDir, outputs); err != nil {
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		sw, err := netdiagram.Parse(vendor, ip, outputs)
		if err != nil {
			res.Error = err.Error() + ". Raw output saved in " + rawDir
			results = append(results, res)
			continue
		}
		res.OK, res.Model = true, sw.Model
		results = append(results, res)
		switches = append(switches, sw)
	}
	if len(switches) == 0 {
		return map[string]any{"switches": results}, nil
	}

	g.mu.Lock()
	var arp []netdiagram.ARP
	for _, h := range g.last.Hosts {
		if h.MAC != "" {
			arp = append(arp, netdiagram.ARP{MAC: h.MAC, IP: h.IP})
		}
	}
	g.mu.Unlock()
	snap := netdiagram.Build(switches, db, oui, arp, netdiagram.LocalMACs())
	// Ask the devices sharing a port whether they're access points (SSH banner, web server).
	if fp := netdiagram.FingerprintAPs(snap); len(fp) > 0 {
		netdiagram.MarkAccessPoints(&snap, db, fp)
	}
	netdiagram.MarkGateways(&snap, db, netdiagram.LocalGateways())

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	file := filepath.Join(dir, snap.Taken.Format("2006-01-02_150405")+".json")
	b, _ := json.MarshalIndent(snap, "", "  ")
	if err := os.WriteFile(file, b, 0o644); err != nil {
		return nil, err
	}
	return map[string]any{"switches": results, "snapshot": snap, "svg": netdiagram.SVG(snap), "file": file}, nil
}

func (g *guiServer) snapshots(req map[string]any) (any, error) {
	dir := str(req["dir"])
	if dir == "" {
		dir = g.dir
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	type entry struct {
		File string `json:"file"`
		Name string `json:"name"`
	}
	out := []entry{}
	for _, f := range files {
		out = append(out, entry{f, filepath.Base(f)})
	}
	return out, nil
}

func (g *guiServer) snapshot(req map[string]any) (any, error) {
	file := str(req["file"])
	if !strings.EqualFold(filepath.Ext(file), ".json") {
		return nil, fmt.Errorf("not a snapshot: %s", file)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var snap netdiagram.Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return map[string]any{"snapshot": snap, "svg": netdiagram.SVG(snap), "file": file}, nil
}

func findPinger() string {
	name := "pinger"
	if runtime.GOOS == "windows" {
		name = "pinger.exe"
	}
	return findPingerFile(name)
}

// defaultSnapshotDir is Documents\net-diagram, or Documents\netdiagram (where v1.0.0 saved them)
// while that one exists and the new one doesn't, so earlier reads stay in the list.
func defaultSnapshotDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "net-diagram-snapshots"
	}
	dir := filepath.Join(home, "Documents", "net-diagram")
	if older := filepath.Join(home, "Documents", "netdiagram"); !exists(dir) && exists(older) {
		return older
	}
	return dir
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func openBrowser(url string) {
	if runtime.GOOS == "windows" {
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
		return
	}
	exec.Command("xdg-open", url).Start()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func contains(s []string, x string) bool {
	for _, y := range s {
		if y == x {
			return true
		}
	}
	return false
}
