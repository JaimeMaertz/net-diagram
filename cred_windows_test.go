//go:build windows

package netdiagram

import (
	"os/exec"
	"testing"
)

// Writes a throwaway login, reads it back, and removes it with cmdkey.
func TestCredentialRoundTrip(t *testing.T) {
	const target = "net-diagram:test-round-trip-delete-me"
	defer exec.Command("cmdkey", "/delete:"+target).Run()
	if err := WriteCredential(target, "tester", "pässwörd-123"); err != nil {
		t.Fatal(err)
	}
	user, pw, err := ReadCredential(target)
	if err != nil {
		t.Fatal(err)
	}
	if user != "tester" || pw != "pässwörd-123" {
		t.Errorf("read back %q / (%d chars), want tester / the same password", user, len(pw))
	}
}
