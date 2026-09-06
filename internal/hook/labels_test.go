package hook

import "testing"

// The command labels on the live board were three rows of the same truncated
// scratch path — three different jobs, none distinguishable.
func TestCommandLabelKeepsTheProgramNotThePath(t *testing.T) {
	cases := map[string]string{
		"go test ./... -race":                                                                  "go test",
		"T=/private/tmp/claude-502/-Users-x/y/z bash run":                                      "bash run",
		"SP=/tmp/a prev=\"\" /usr/bin/env python3 thing.py":                                    "env python3",
		"/private/tmp/claude-502/-Users-gavin-jeong-src-sendbird-mitm-proxy/260a6668/probe.sh": "probe.sh",
		"prev=\"\"": "prev=\"\"",
	}
	for command, want := range cases {
		if got := commandLabel(command); got != want {
			t.Errorf("commandLabel(%q) = %q, want %q", command, got, want)
		}
	}
}

// A description is a human summary; a command line is not. When both exist
// the description wins.
func TestBackgroundLabelPrefersADescription(t *testing.T) {
	bt := BackgroundTask{Type: "shell", Description: "race test",
		Command: "/private/tmp/claude-502/-Users-x/probe.sh"}
	if got := backgroundLabel(bt); got != "race test" {
		t.Errorf("backgroundLabel = %q, want the description", got)
	}
}
