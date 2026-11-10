package netdiagram

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// DBEntry is one entry of the device database (pinger's devices.json). Only mac is required.
type DBEntry struct {
	MAC       string `json:"mac"`
	Name      string `json:"name,omitempty"`
	Model     string `json:"model,omitempty"`
	Jobsite   string `json:"jobsite,omitempty"`
	Interface string `json:"interface,omitempty"` // which interface of the device this MAC is
	Type      string `json:"type,omitempty"`      // switch, processor, panel, encoder…
}

// DeviceDB is the device database keyed by normalized MAC.
type DeviceDB map[string]DBEntry

// LoadDeviceDB reads devices.json. A missing file is an empty database.
func LoadDeviceDB(path string) (DeviceDB, error) {
	db := DeviceDB{}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return db, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []DBEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, e := range entries {
		m, ok := NormalizeMAC(e.MAC)
		if !ok {
			return nil, fmt.Errorf("%s: %q is not a MAC address", path, e.MAC)
		}
		e.MAC = m
		db[m] = e
	}
	return db, nil
}

// OUI maps a MAC's first three bytes ("C44268") to its manufacturer.
type OUI map[string]string

// LoadOUI reads pinger's oui.txt (six hex digits, a tab, the manufacturer). A missing file is an
// empty table.
func LoadOUI(path string) (OUI, error) {
	o := OUI{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return o, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "\t"); ok {
			o[strings.ToUpper(k)] = v
		}
	}
	return o, sc.Err()
}

// Lookup returns the manufacturer of mac (normalized), or "".
func (o OUI) Lookup(mac string) string {
	return o[strings.ReplaceAll(mac, ":", "")[:6]]
}

// NormalizeMAC accepts colons, dashes, dots (Cisco "c442.6812.3456") or no separators, either
// case, and returns "C4:42:68:12:34:56".
func NormalizeMAC(s string) (string, bool) {
	h := strings.NewReplacer(":", "", "-", "", ".", "").Replace(strings.TrimSpace(s))
	if len(h) != 12 {
		return "", false
	}
	h = strings.ToUpper(h)
	for _, c := range h {
		if !strings.ContainsRune("0123456789ABCDEF", c) {
			return "", false
		}
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String(), true
}
