package transcript

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

// Facts is what the session's own transcript already knows about
// itself, read from the tail of the JSONL file the hook payload points at.
//
// This exists because deriving a title from the prompt was wrong. Claude Code
// writes an `ai-title` record — a summary of what the session is *about* —
// and refreshes it as the work moves. The first prompt is a much worse name:
// measured on a live board of 14 items, 6 titles were unreadable (bare UUIDs
// from cross-session wake-ups, a pasted Slack URL, "/sb:pr-followup", and two
// blanks), while every one of 20 sampled sessions had an ai-title.
//
// It also carries `pr-link`, so the PR a session opened needs no discovery at
// all — 12 of those 20 sessions had one recorded.
type Facts struct {
	Title     string
	Branch    string
	PRNumber  int
	PRURL     string
	PRRepo    string
	LastPromp string
}

// tailBytes is how much of the transcript to read from the end.
//
// The records we want are written near the end and rewritten as the session
// moves: across 12 sampled transcripts (57 to 9,919 lines) the last `ai-title`
// was never more than 27 lines from the end. 256 KiB covers that with room to
// spare and keeps the read at a few milliseconds even on a 3 MB file, which is
// what makes this safe to do on every hook invocation.
const tailBytes = 256 << 10

// Read pulls the facts out of the tail of path. Every failure is a
// silent empty result: a transcript that is missing, truncated, or half-written
// is not a reason to fail a hook.
func Read(path string) Facts {
	var f Facts
	if path == "" {
		return f
	}
	file, err := os.Open(path)
	if err != nil {
		return f
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return f
	}
	offset := info.Size() - tailBytes
	if offset < 0 {
		offset = 0
	}
	buf := make([]byte, info.Size()-offset)
	if _, err := file.ReadAt(buf, offset); err != nil && len(buf) == 0 {
		return f
	}

	lines := strings.Split(string(buf), "\n")
	// A mid-file offset almost certainly lands inside a line; that fragment is
	// not valid JSON and would only be skipped, but dropping it is cheaper.
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:]
	}

	// Later records supersede earlier ones — the title is rewritten as the
	// session's subject drifts — so scan forward and keep overwriting.
	var rec struct {
		Type       string `json:"type"`
		AITitle    string `json:"aiTitle"`
		LastPrompt string `json:"lastPrompt"`
		GitBranch  string `json:"gitBranch"`
		PRNumber   int    `json:"prNumber"`
		PRURL      string `json:"prUrl"`
		PRRepo     string `json:"prRepository"`
	}
	for _, line := range lines {
		if len(line) < 2 || line[0] != '{' {
			continue
		}
		rec = struct {
			Type       string `json:"type"`
			AITitle    string `json:"aiTitle"`
			LastPrompt string `json:"lastPrompt"`
			GitBranch  string `json:"gitBranch"`
			PRNumber   int    `json:"prNumber"`
			PRURL      string `json:"prUrl"`
			PRRepo     string `json:"prRepository"`
		}{}
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		switch rec.Type {
		case "ai-title":
			if rec.AITitle != "" {
				f.Title = rec.AITitle
			}
		case "last-prompt":
			if rec.LastPrompt != "" {
				f.LastPromp = rec.LastPrompt
			}
		case "pr-link":
			if rec.PRURL != "" {
				f.PRNumber, f.PRURL, f.PRRepo = rec.PRNumber, rec.PRURL, rec.PRRepo
			}
		}
		// gitBranch rides along on ordinary turn records rather than having a
		// type of its own, so take it wherever it appears.
		if rec.GitBranch != "" && rec.GitBranch != "HEAD" {
			f.Branch = rec.GitBranch
		}
	}
	return f
}

// ticketPattern matches the JIRA keys used here. The PR guard requires the
// ticket in the branch name, so the branch is the one place it is guaranteed
// to appear — and it is there before any PR exists, which is what makes it a
// better grouping key than the PR.
var ticketPattern = regexp.MustCompile(`\b([A-Z][A-Z0-9]+-\d+)\b`)

// TicketFrom pulls a JIRA key out of a branch name.
//
// Both separators in use are handled by the same pattern: `CPLAT-11964/desc`
// and `CPLAT-11993-gestalt-image-tag`. A branch with no key — `main`,
// `follow-the-work` — yields nothing, and that session simply stays grouped by
// repository.
func TicketFrom(branch string) string {
	m := ticketPattern.FindStringSubmatch(branch)
	if m == nil {
		return ""
	}
	return m[1]
}
