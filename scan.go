package netdiagram

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
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
	Vendor    string       `json:"vendor,omitempty"`     // Netgear, Cisco, TP-Link, Aruba: a switch maker
	Supported bool         `json:"supported,omitempty"`  // net-diagram can read this vendor today
	Candidate bool         `json:"candidate,omitempty"`  // a switch maker with telnet or SSH open
	Ports     map[int]bool `json:"ports,omitempty"`      // 22, 23: TCP open; 80, 443: an HTTP status came back
	HasLogin  bool         `json:"has_login,omitempty"`  // a login is saved for it
	SSHBanner string       `json:"ssh_banner,omitempty"` // the SSH server's first line, read for TP-Link
	Note      string       `json:"note,omitempty"`       // why a switch maker's host isn't a candidate
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
	{regexp.MustCompile(`(?i)tp-?link`), "TP-Link", true},
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
		e, inDB := db[h.MAC]
		isSwitch := inDB && strings.EqualFold(e.Type, "switch")
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.Ports = ProbeHost(h.IP)
			h.Candidate = h.Ports[22] || h.Ports[23]
			if h.Vendor == "TP-Link" && !isSwitch {
				// TP-Link makes access points, routers and plugs too. Its JetStream switches run
				// "IPSSH"; its access points and routers run dropbear. Telnet is a switch's.
				if h.Ports[22] {
					h.SSHBanner = SSHBanner(h.IP, 3*time.Second)
				}
				h.Candidate = h.Ports[23] || strings.Contains(h.SSHBanner, "IPSSH")
				if !h.Candidate && h.SSHBanner != "" {
					h.Note = "not a switch (SSH server " + h.SSHBanner + ")"
				}
			}
		}()
		if h.MAC != "" && hasLogin != nil {
			h.HasLogin = hasLogin(h.MAC)
		}
	}
	wg.Wait()
}

// WebTimeout is how long a web check waits for a status line.
const WebTimeout = 3 * time.Second

// ProbeHost fills a switch's port badges: SSH and Telnet by a TCP connection (800 ms), Web and HTTPS
// by WebAnswers, all at once. A TCP connection alone doesn't prove a web server: on a phone hotspot (and any
// network with a transparent proxy) ports 80 and 443 accept the connection for every address, even an
// unused one, and then never answer.
func ProbeHost(ip string) map[int]bool {
	open := ProbePorts(ip, []int{22, 23}, 800*time.Millisecond)
	var web, secure bool
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); web = WebAnswers("http://"+ip+"/", WebTimeout) }()
	go func() { defer wg.Done(); secure = WebAnswers("https://"+ip+"/", WebTimeout) }()
	wg.Wait()
	open[80], open[443] = web, secure
	return open
}

// webClient sends the web checks: no redirects followed, no proxy, any certificate accepted (AV and
// network devices carry self-signed ones), no kept connections.
var webClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{
		Proxy:             nil,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
	},
}

// WebAnswers reports whether a GET to url comes back with any HTTP status (200, a redirect, 401, 404 all
// count) within the timeout.
func WebAnswers(url string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := webClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// SSHBanner returns the first line an SSH server sends ("SSH-1.99-IPSSH-6.6.0"), without the
// "SSH-x.y-" prefix, or "" when none arrives in time.
func SSHBanner(ip string, timeout time.Duration) string {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "22"), timeout)
	if err != nil {
		return ""
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(timeout))
	line, _ := bufio.NewReader(c).ReadString('\n')
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "SSH-") {
		return ""
	}
	if i := strings.Index(line[4:], "-"); i >= 0 {
		return line[4+i+1:]
	}
	return line
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
