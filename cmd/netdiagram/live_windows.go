//go:build windows

package main

import "github.com/JaimeMaertz/netdiagram"

// collectLive logs into a switch with its saved login and runs its vendor's commands.
func collectLive(vendor, host, cred string) (map[string]string, error) {
	user, pw, err := netdiagram.ReadCredential(cred)
	if err != nil {
		return nil, err
	}
	return netdiagram.Collect(vendor, host, user, pw)
}
