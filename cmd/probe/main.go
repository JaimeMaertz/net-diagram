//go:build windows

// probe logs into one switch with its saved login, runs every allowlisted command for its vendor
// (and the per-port commands those call for) and saves each output raw to a folder, for building
// and testing parsers.
//
//	probe -vendor Netgear -host 172.22.0.4 -cred net-diagram:28:94:01:AF:35:BD -out testdata\m4250
//	probe -vendor TP-Link -host 192.168.1.100 -cred net-diagram:<MAC> -out testdata\tl-sg3428xmp
//	probe -vendor Cisco -host 172.22.0.100 -cred net-diagram:<MAC> -out testdata\c1300
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/JaimeMaertz/net-diagram"
)

func main() {
	vendor := flag.String("vendor", "Netgear", "Netgear, Cisco or TP-Link")
	host := flag.String("host", "", "switch address")
	cred := flag.String("cred", "", "Credential Manager target holding the login")
	out := flag.String("out", "", "folder for the raw output")
	flag.Parse()
	if *host == "" || *cred == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	user, pw, err := netdiagram.ReadCredential(*cred)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	outputs, err := netdiagram.Collect(*vendor, *host, user, pw)
	pw = ""
	if len(outputs) > 0 {
		if serr := netdiagram.SaveOutputs(*out, outputs); serr != nil {
			fmt.Fprintln(os.Stderr, serr)
			os.Exit(1)
		}
	}
	cmds := make([]string, 0, len(outputs))
	for c := range outputs {
		cmds = append(cmds, c)
	}
	sort.Strings(cmds)
	for _, c := range cmds {
		o := outputs[c]
		status := fmt.Sprintf("%d lines", strings.Count(o, "\n")+1)
		if strings.HasPrefix(o, "% netdiagram:") {
			status = "REFUSED " + strings.TrimPrefix(o, "% netdiagram: ")
		}
		fmt.Printf("%-44s %s\n", c, status)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("%d outputs saved to %s\n", len(outputs), *out)
}
