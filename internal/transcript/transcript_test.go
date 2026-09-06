package transcript

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
	got := Read(path)
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
	got := Read(path)
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
	if got := Read(path).Branch; got != "" {
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
			got := Read(path) // must not panic
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

	got := Read(path)
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
	got := Read(writeTranscript(t, lines...))
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
		if got := TicketFrom(branch); got != want {
			t.Errorf("TicketFrom(%q) = %q, want %q", branch, got, want)
		}
	}
}

// Branches are typed by hand and the case slips: cplat-11794-count-... and
// CPLAT-11794/cursor-cli-... are the same ticket in the same repo. Requiring
// uppercase silently dropped the first, so the two never grouped.
func TestTicketFromIsCaseInsensitiveAndNormalizes(t *testing.T) {
	cases := map[string]string{
		"cplat-11794-count-abandoned-handshakes": "CPLAT-11794",
		"CPLAT-11794/cursor-cli-connector-alias": "CPLAT-11794",
		"Cplat-11794-mixed":                      "CPLAT-11794",
		"AIML-121-jarvis-tooling-doppler":        "AIML-121",
		"DINF-4659":                              "DINF-4659",
	}
	for branch, want := range cases {
		if got := TicketFrom(branch); got != want {
			t.Errorf("TicketFrom(%q) = %q, want %q", branch, got, want)
		}
	}
}

// A project prefix with no number is not a ticket, and a detached checkout
// records prose rather than a branch name. Neither must produce a match.
func TestTicketFromRejectsNonTickets(t *testing.T) {
	for _, branch := range []string{
		"CPLAT-zerosoda-mirror",        // prefix, no number
		"(HEAD detached at 9d834e4d2)", // not a branch name at all
		"(HEAD detached at origin/main)",
		"main", "master", "auto-add-workspace", "cohome",
		"share-the-cache", "follow-the-work", "",
	} {
		if got := TicketFrom(branch); got != "" {
			t.Errorf("TicketFrom(%q) = %q, want no match", branch, got)
		}
	}
}
