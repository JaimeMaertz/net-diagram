package netdiagram

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// testdata/two-switches/snapshot.json is a live read of a test bench (MACs, serials and names altered): an M4250-10G2F-PoE+
// (172.22.0.4) whose port 0/1 runs to port 0/26 of an M4250-26G4F-PoE+ (172.22.0.157), with
// devices on both.

var (
	reRect     = regexp.MustCompile(`<rect x="([\d.-]+)" y="([\d.-]+)" width="([\d.]+)" height="([\d.]+)" rx="4" class="(?:dev|unread)[^"]*"/>`)
	reTrunk    = regexp.MustCompile(`<polyline points="([^"]+)" class="trunk"/>`)
	reWire     = regexp.MustCompile(`<polyline points="([^"]+)" class="wire"/>`)
	reCurve    = regexp.MustCompile(`<path d="M[^"]*" class="trunk"/>`)
	rePortRect = regexp.MustCompile(`<rect x="([\d.]+)" y="([\d.]+)" width="18" height="18" class="port[^"]*"><title>([^ <]+)`)
)

func points(s string) [][2]float64 {
	var out [][2]float64
	for _, p := range strings.Fields(s) {
		xy := strings.Split(p, ",")
		x, _ := strconv.ParseFloat(xy[0], 64)
		y, _ := strconv.ParseFloat(xy[1], 64)
		out = append(out, [2]float64{x, y})
	}
	return out
}

// segmentHitsRect: an axis-aligned segment passes through the inside of a rectangle.
func segmentHitsRect(a, b [2]float64, x, y, w, h float64) bool {
	x1, x2 := min(a[0], b[0]), max(a[0], b[0])
	y1, y2 := min(a[1], b[1]), max(a[1], b[1])
	return x1 < x+w && x2 > x && y1 < y+h && y2 > y
}

func TestSVGLinkBetweenReadSwitches(t *testing.T) {
	b, err := os.ReadFile("testdata/two-switches/snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	out := SVG(snap)

	if reCurve.MatchString(out) {
		t.Error("the link is drawn as a curve")
	}
	trunks := reTrunk.FindAllStringSubmatch(out, -1)
	if len(trunks) != 1 {
		t.Fatalf("want one right-angled trunk polyline for the one link, got %d", len(trunks))
	}
	route := points(trunks[0][1])
	for _, r := range reRect.FindAllStringSubmatch(out, -1) {
		x, _ := strconv.ParseFloat(r[1], 64)
		y, _ := strconv.ParseFloat(r[2], 64)
		w, _ := strconv.ParseFloat(r[3], 64)
		h, _ := strconv.ParseFloat(r[4], 64)
		for i := 0; i+1 < len(route); i++ {
			if segmentHitsRect(route[i], route[i+1], x, y, w, h) {
				t.Errorf("the trunk crosses the device box at %v,%v", x, y)
			}
		}
	}

	// Neither end of the link gets an ordinary device wire (nothing was placed on those ports).
	// A wire starts at its port's bottom centre: x + 9, and 4 px below the square (y + 22).
	portAt := map[[2]float64]string{}
	var linkEnds [][2]float64
	for _, m := range rePortRect.FindAllStringSubmatch(out, -1) {
		x, _ := strconv.ParseFloat(m[1], 64)
		y, _ := strconv.ParseFloat(m[2], 64)
		at := [2]float64{x + 9, y + 22}
		portAt[at] = m[3]
		if strings.Contains(m[0], " link") {
			linkEnds = append(linkEnds, at)
		}
	}
	if len(linkEnds) != 2 {
		t.Fatalf("want both link ports marked, got %d", len(linkEnds))
	}
	for _, w := range reWire.FindAllStringSubmatch(out, -1) {
		start := points(w[1])[0]
		for _, end := range linkEnds {
			if start == end {
				t.Errorf("a device wire leaves link port %s", portAt[end])
			}
		}
	}
	if !strings.Contains(out, "0/1 ↔ 0/26") {
		t.Error("the trunk isn't labelled with its two ports")
	}
}
