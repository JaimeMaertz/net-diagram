package ciscossh

import (
	"testing"
)

func TestPrompt(t *testing.T) {
	for _, s := range []string{"\nC1300#", "\nC1300# ", "\nC1300(config)#", "\nSW-01(config-if)#", "C1300>"} {
		if !promptRe.MatchString(s) {
			t.Errorf("%q should be a prompt", s)
		}
	}
	for _, s := range []string{"\n description lab #", "\nvlan 10 name AV", "\nC1300#show vlan"} {
		if promptRe.MatchString(s) {
			t.Errorf("%q should not be a prompt", s)
		}
	}
	if !confirmRe.MatchString("Overwrite file [startup-config].... (Y/N)[N] ?") {
		t.Error("Y/N question not matched")
	}
}

func TestErrorLines(t *testing.T) {
	for line, want := range map[string]bool{
		"% Unrecognized command":                                      true,
		"   % bad parameter value":                                    true,
		"%AAA-E-SOMETHING: failed":                                    true,
		"%COPY-I-FILECPY: Files Copy - source URL":                    false,
		"%COPY-N-TRAP: The copy operation was completed successfully": false,
		" description 100%":                                           false,
	} {
		if got := errorRe.MatchString(line); got != want {
			t.Errorf("%q: error = %v, want %v", line, got, want)
		}
	}
}

func TestOutput(t *testing.T) {
	text := "show vlan\nVlan  Name\n----  ----\n1     1\nMore: <space>,  Quit: q\n10    AV\n\nC1300#"
	got := output(text, "show vlan")
	want := "Vlan  Name\n----  ----\n1     1\n10    AV"
	if got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if got := clean("ab\x08c\r\n\x1b[2Kd"); got != "ac\nd" {
		t.Errorf("clean = %q", got)
	}
}
