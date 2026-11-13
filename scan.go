package netdiagram

import (
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Host is one address that answered a sweep.
type Host struct {
	IP           string  `json:"ip"`
	Replied      bool    `json:"replied"`
	RTTms        float64 `json:"rtt_ms,omitempty"`
	MAC          string  `json:"mac,omitempty"`
	Manufacturer string  `json:"manufacturer,omitempty"`
	Name         string  `json:"name,omitempty"`
	Model        string  `json:"model,omitempty"`
	Jobsite      string  `json:"jobsite,omitempty"`

	// Filled in by FindSwitches.
	Vendor    string       `json:"vendor,omitempty"`    // Netgear, Cisco, Aruba: a switch maker
	Supported bool         `json:"supported,omitempty"` // net-diagram can read this vendor today
	Candidate bool         `json:"candidate,omitempty"` // a switch maker with telnet or SSH open
	Ports     map[int]bool `json:"ports,omitempty"`     // 22, 23, 80, 443: open or not
	HasLogin  bool         `json:"has_login,omitempty"` // a login is saved for it
}

// Sweep is pinger's -json output.
type Sweep struct {
	Hosts      []Host   `json:"hosts"`
	Targets    int      `json:"targets"`
	Seconds    float64  `json:"seconds"`
	ARPCleared bool     `json:"arp_cleared"`
	Skipped    []string `json:"skipped,omitempty"`
}

// RunSweep runs pinger (its -json output) over the subnets. The ARP cache is left alone, since
// clearing it needs an Administrator prompt.
func RunSweep(pinger string, subnets []string) (Sweep, error) {
	var s Sweep
	args := append([]string{"-json", "-no-clear"}, subnets...)
	out, err := exec.Command(pinger, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(out) == 0 {
			return s, fmt.Errorf("pinger: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		if len(out) == 0 {
			return s, fmt.Errorf("pinger: %w", err)
		}
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return s, fmt.Errorf("pinger's output: %w", err)
	}
	return s, nil
}

var switchMakers = []struct {
	re        *regexp.Regexp
	vendor    string
	supported bool
}{
	{regexp.MustCompile(`(?i)netgear`), "Netgear", true},
	{regexp.MustCompile(`(?i)cisco`), "Cisco", true},
	{regexp.MustCompile(`(?i)aruba|hewlett|\bhpe\b`), "Aruba", false},
}

// FindSwitches marks the hosts that look like switches: made by a switch maker (or typed
// "switch" in the database) with telnet or SSH open. Ports are probed for those makers only.
// hasLogin reports whether a login is saved for a MAC.
func FindSwitches(hosts []Host, db DeviceDB, hasLogin func(mac string) bool) {
	var wg sync.WaitGroup
	for i := range hosts {
		h := &hosts[i]
		maker := h.Manufacturer
		if e, ok := db[h.MAC]; ok && strings.EqualFold(e.Type, "switch") && maker == "" {
			maker = e.Model
		}
		for _, m := range switchMakers {
			if m.re.MatchString(maker) {
				h.Vendor, h.Supported = m.vendor, m.supported
				break
			}
		}
		if e, ok := db[h.MAC]; ok && strings.EqualFold(e.Type, "switch") && h.Vendor == "" {
			h.Vendor = "unknown"
		}
		if h.Vendor == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.Ports = ProbePorts(h.IP, []int{22, 23, 80, 443}, 800*time.Millisecond)
			h.Candidate = h.Ports[22] || h.Ports[23]
		}()
		if h.MAC != "" && hasLogin != nil {
			h.HasLogin = hasLogin(h.MAC)
		}
	}
	wg.Wait()
}

// ProbePorts tries a TCP connection to each port at once.
func ProbePorts(ip string, ports []int, timeout time.Duration) map[int]bool {
	open := map[int]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range ports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, fmt.Sprint(p)), timeout)
			mu.Lock()
			open[p] = err == nil
			mu.Unlock()
			if err == nil {
				c.Close()
			}
		}()
	}
	wg.Wait()
	return open
}

// CredTarget is the Credential Manager name a switch's login is saved under.
func CredTarget(mac string) string { return "net-diagram:" + mac }

// LoginTarget is the Credential Manager name to read a switch's login from: CredTarget, or
// "netdiagram:<MAC>", the name v1.0.0 saved logins under.
func LoginTarget(mac string) string {
	if !credExists(CredTarget(mac)) && credExists("netdiagram:"+mac) {
		return "netdiagram:" + mac
	}
	return CredTarget(mac)
}

func credExists(target string) bool {
	_, _, err := ReadCredential(target)
	return err == nil
}

// LocalMACs maps each of this PC's adapter MACs to its IPv4 addresses: how the PC the scan runs
// from is recognized on the switches, as "Your Computer".
func LocalMACs() map[string][]string {
	out := map[string][]string{}
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		mac, ok := NormalizeMAC(ifc.HardwareAddr.String())
		if !ok || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		out[mac] = append(out[mac], []string{}...)
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLinkLocalUnicast() {
				out[mac] = append(out[mac], n.IP.String())
			}
		}
	}
	return out
}

// LocalSubnets lists a /24 around each IPv4 address this PC has on a physical or USB adapter,
// leaving out loopback, link-local and WSL's virtual adapter.
func LocalSubnets() []string {
	var out []string
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || strings.Contains(ifc.Name, "WSL") {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil || n.IP.IsLinkLocalUnicast() {
				continue
			}
			ip := n.IP.To4()
			out = append(out, fmt.Sprintf("%d.%d.%d.0/24", ip[0], ip[1], ip[2]))
		}
	}
	return uniq(out)
}
