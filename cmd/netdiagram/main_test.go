package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The database is the one beside the pinger on PATH, ahead of the repo's ../pinger copy.
func TestFindPingerFileUsesPingerOnPath(t *testing.T) {
	dir := t.TempDir()
	exe := "pinger"
	if runtime.GOOS == "windows" {
		exe = "pinger.exe"
	}
	for _, f := range []string{exe, "devices.json"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	if got, want := findPingerFile("devices.json"), filepath.Join(dir, "devices.json"); got != want {
		t.Errorf("devices.json: got %s, want %s", got, want)
	}
	if got, want := findPingerFile(exe), filepath.Join(dir, exe); got != want {
		t.Errorf("%s: got %s, want %s", exe, got, want)
	}
	// A file the PATH folder doesn't have comes from the ../pinger search, and when that has none
	// either, the answer is beside the pinger on PATH, where it belongs.
	if beside := findBeside("oui.txt"); beside != "" {
		if got := findPingerFile("oui.txt"); got != beside {
			t.Errorf("oui.txt: got %s, want the ../pinger copy %s", got, beside)
		}
	} else if got, want := findPingerFile("oui.txt"), filepath.Join(dir, "oui.txt"); got != want {
		t.Errorf("oui.txt found nowhere: got %s, want %s", got, want)
	}

	t.Setenv("PATH", t.TempDir())
	if got := findPingerFile("devices.json"); got == filepath.Join(dir, "devices.json") {
		t.Error("no pinger on PATH: still used the old folder")
	}
}
