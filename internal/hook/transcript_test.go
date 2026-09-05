package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTranscript builds a JSONL file from the record types Claude Code
// actually writes, in the order they appear.
func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// The board's titles come from here. Every one of 20 sampled sessions had an
// ai-title; naming items from prompts instead produced 6 unreadable titles
// out of 14 on the live board.
func TestReadTranscriptTakesTheLatestTitle(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"ai-title","aiTitle":"first guess","sessionId":"s1"}`,
		`{"type":"user","gitBranch":"CPLAT-11964/infra-provisioner","cwd":"/src"}`,
		`{"type":"ai-title","aiTitle":"what it turned out to be","sessionId":"s1"}`,
	)
	got := readTranscript(path)
	if got.Title != "what it turned out to be" {
		t.Errorf("Title = %q, want the last one written", got.Title)
	}
	if got.Branch != "CPLAT-11964/infra-provisioner" {
		t.Errorf("Branch = %q", got.Branch)
	}
}

// pr-link means the PR needs no discovery: the session recorded it.
func TestReadTranscriptFindsThePR(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"pr-link","sessionId":"s1","prNumber":932,"prUrl":"https://github.com/o/r/pull/932","prRepository":"o/r"}`,
	)
	got := readTranscript(path)
	if got.PRNumber != 932 || got.PRRepo != "o/r" {
		t.Errorf("pr = %d %q, want 932 o/r", got.PRNumber, got.PRRepo)
	}
}

// "HEAD" is what a detached checkout records. It is not a branch name and
// must not become one, or every worktree session groups under "HEAD".
func TestReadTranscriptRejectsDetachedHead(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"user","gitBranch":"HEAD","cwd":"/src"}`,
	)
	if got := readTranscript(path).Branch; got != "" {
		t.Errorf("Branch = %q, want empty for a detached HEAD", got)
	}
}

// A hook must never fail on a transcript, however broken.
func TestReadTranscriptSurvivesBadInput(t *testing.T) {
	cases := map[string]string{
		"missing":    filepath.Join(t.TempDir(), "nope.jsonl"),
		"empty path": "",
		"not json":   writeTranscript(t, "this is not json", "{also not"),
		"empty file": writeTranscript(t, ""),
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			got := readTranscript(path) // must not panic
			if got.Title != "" {
				t.Errorf("got a title from %s: %q", name, got.Title)
			}
		})
	}
}

// Only the tail is read, so a huge transcript stays cheap. The records we
// want are always near the end — measured at most 27 lines from it across 12
// real transcripts.
func TestReadTranscriptReadsOnlyTheTail(t *testing.T) {
	var lines []string
	lines = append(lines, `{"type":"ai-title","aiTitle":"ancient history"}`)
	filler := `{"type":"assistant","message":"` + strings.Repeat("x", 400) + `"}`
	for i := 0; i < 2000; i++ {
		lines = append(lines, filler)
	}
	lines = append(lines, `{"type":"ai-title","aiTitle":"current"}`)
	path := writeTranscript(t, lines...)

	got := readTranscript(path)
	if got.Title != "current" {
		t.Errorf("Title = %q, want the recent one", got.Title)
	}
}

// A read that starts mid-file lands inside a line. That fragment is not JSON
// and must be dropped rather than corrupting a field.
func TestReadTranscriptDiscardsThePartialFirstLine(t *testing.T) {
	var lines []string
	filler := `{"type":"assistant","message":"` + strings.Repeat("y", 500) + `"}`
	for i := 0; i < 1000; i++ {
		lines = append(lines, filler)
	}
	lines = append(lines, `{"type":"ai-title","aiTitle":"intact"}`)
	got := readTranscript(writeTranscript(t, lines...))
	if got.Title != "intact" {
		t.Errorf("Title = %q", got.Title)
	}
}

// Both branch conventions in use here carry the ticket.
func TestTicketFromBranch(t *testing.T) {
	cases := map[string]string{
		"CPLAT-11964/infra-provisioner-v0-1-22": "CPLAT-11964",
		"CPLAT-11993-gestalt-image-tag":         "CPLAT-11993",
		"DINF-4659":                             "DINF-4659",
		"main":                                  "",
		"follow-the-work":                       "",
		"":                                      "",
		"feature/no-ticket-here":                "",
	}
	for branch, want := range cases {
		if got := ticketFrom(branch); got != want {
			t.Errorf("ticketFrom(%q) = %q, want %q", branch, got, want)
		}
	}
}

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
