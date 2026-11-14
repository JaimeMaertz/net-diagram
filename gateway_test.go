package netdiagram

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestParseRoutePrint(t *testing.T) {
	// Windows "route print -4 0.0.0.0", with an on-link default and a persistent route added.
	out := "===========================================================================\r\n" +
		"IPv4 Route Table\r\n" +
		"===========================================================================\r\n" +
		"Active Routes:\r\n" +
		"Network Destination        Netmask          Gateway       Interface  Metric\r\n" +
		"          0.0.0.0          0.0.0.0      192.168.1.1    192.168.1.193     35\r\n" +
		"          0.0.0.0          0.0.0.0         On-link     172.22.0.200    281\r\n" +
		"          0.0.0.0          0.0.0.0       10.20.0.1       10.20.0.50     40\r\n" +
		"===========================================================================\r\n" +
		"Persistent Routes:\r\n" +
		"  Network Address          Netmask  Gateway Address  Metric\r\n" +
		"          0.0.0.0          0.0.0.0       10.99.0.1  Default\r\n"
	if got := parseRoutePrint(out); !reflect.DeepEqual(got, []string{"192.168.1.1", "10.20.0.1"}) {
		t.Errorf("parseRoutePrint = %v", got)
	}
}

func TestParseProcRoute(t *testing.T) {
	out := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t0101A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
		"eth0\t0001A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n"
	if got := parseProcRoute(out); !reflect.DeepEqual(got, []string{"192.168.1.1"}) {
		t.Errorf("parseProcRoute = %v", got)
	}
}

// On the network the fixtures came from, the default gateway is the device on the XMP's Gi1/0/15.
func TestMarkGateways(t *testing.T) {
	s := buildChain(t)
	var gw *Device
	for i := range s.Devices {
		if s.Devices[i].Port == "Gi1/0/15" {
			gw = &s.Devices[i]
		}
	}
	if gw == nil || !strings.HasPrefix(gw.Interfaces[0].MAC, "64:62:66") {
		t.Fatalf("no device on Gi1/0/15: %+v", gw)
	}
	gw.Interfaces[0].IP = "192.0.2.1"
	MarkGateways(&s, DeviceDB{}, []string{"192.0.2.1"})
	n := 0
	for _, d := range s.Devices {
		if d.Gateway != "" {
			n++
		}
	}
	if gw.Gateway != "this PC's default gateway" || n != 1 {
		t.Errorf("gateway %q, %d devices marked", gw.Gateway, n)
	}
	if !strings.Contains(SVG(s), `class="dev gw"`) || !strings.Contains(SVG(s), "gateway · this PC") {
		t.Error("the gateway isn't drawn as one")
	}
	// A saved snapshot keeps its gateways: marking with nil gives the same answer.
	MarkGateways(&s, DeviceDB{}, nil)
	if gw.Gateway == "" || !reflect.DeepEqual(s.Gateways, []string{"192.0.2.1"}) {
		t.Errorf("after a nil re-mark: %q %v", gw.Gateway, s.Gateways)
	}
	// The device database can name one too, with no route.
	db := DeviceDB{s.Devices[0].Interfaces[0].MAC: {Type: "Firewall"}}
	MarkGateways(&s, db, []string{})
	if s.Devices[0].Gateway != "device database" || gw.Gateway != "" {
		t.Errorf("database: %q, route-only gateway left as %q", s.Devices[0].Gateway, gw.Gateway)
	}
}

// NETDIAGRAM_LIVE=1 go test -run TestLocalGatewaysLive -v .  prints this PC's default gateways.
func TestLocalGatewaysLive(t *testing.T) {
	if os.Getenv("NETDIAGRAM_LIVE") == "" {
		t.Skip("set NETDIAGRAM_LIVE=1 to read this PC's route table")
	}
	t.Logf("default gateways: %v", LocalGateways())
}
