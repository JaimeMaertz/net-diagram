//go:build !windows

package main

import "errors"

// collectLive needs Windows Credential Manager; elsewhere, use -from with saved output.
func collectLive(vendor, host, cred string) (map[string]string, error) {
	return nil, errors.New("logging in needs Windows (Credential Manager); use -from with saved output")
}
