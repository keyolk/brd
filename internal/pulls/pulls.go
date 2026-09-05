// Package pulls polls GitHub for the pull requests brd's items point at.
//
// This is the one thing the board cannot learn from hooks. The gh CLI takes
// about a second per call, which is far too slow to run at a turn boundary in
// every session — and a PR changes when nobody's session is running at all,
// because review happens on someone else's clock. So the TUI polls while it is
// on screen and writes what it finds; a board nobody is looking at costs
// nothing.
package pulls

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/keyolk/brd/internal/store"
)

// pollTimeout bounds one gh invocation. Measured at ~1s against a live repo;
// 15s allows for a slow network without letting a hung call wedge the poller.
const pollTimeout = 15 * time.Second

// ghPR is the shape gh returns. Only the fields the board renders are read.
type ghPR struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	State          string `json:"state"`
	URL            string `json:"url"`
	HeadRefName    string `json:"headRefName"`
	ReviewDecision string `json:"reviewDecision"`
	UpdatedAt      string `json:"updatedAt"`
	StatusCheck    []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		State      string `json:"state"` // StatusContext uses this instead
	} `json:"statusCheckRollup"`
}

// Poll refreshes the pulls table for every repo the board's items touch.
//
// Errors are returned but never fatal to the caller: a board with a stale PR
// column is worth more than one that stops updating because gh is not logged
// in. Repos are polled one at a time rather than concurrently — this runs on a
// slow timer, and a burst of parallel gh calls is a good way to hit a rate
// limit for no gain.
func Poll(db *store.DB, items []store.Item) error {
	var firstErr error
	for _, slug := range remoteSlugs(items) {
		prs, err := fetch(slug)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, pr := range prs {
			if err := db.PutPull(toPull(slug, pr)); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// remoteSlugs resolves each distinct local repo to its owner/name on GitHub,
// skipping any that has no GitHub remote.
//
// The local directory name is not the slug: `~/src/sendbird/ops-k8s` is
// `sendbird/ops-k8s`, but nothing guarantees that, and a wrong guess queries
// someone else's repository.
func remoteSlugs(items []store.Item) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		if it.Repo == "" || seen[it.Repo] {
			continue
		}
		seen[it.Repo] = true
		if slug := slugFor(it.Repo); slug != "" && !seen[slug] {
			seen[slug] = true
			out = append(out, slug)
		}
	}
	return out
}

func slugFor(repoPath string) string {
	out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return SlugFromRemote(strings.TrimSpace(string(out)))
}

// SlugFromRemote extracts owner/name from either remote URL form.
func SlugFromRemote(url string) string {
	url = strings.TrimSuffix(strings.TrimSpace(url), ".git")
	switch {
	case strings.HasPrefix(url, "git@"):
		_, path, ok := strings.Cut(url, ":")
		if !ok {
			return ""
		}
		url = path
	case strings.Contains(url, "://"):
		_, path, ok := strings.Cut(url, "://")
		if !ok {
			return ""
		}
		// Drop the host, keep the rest.
		if _, rest, ok := strings.Cut(path, "/"); ok {
			url = rest
		} else {
			return ""
		}
	}
	parts := strings.Split(strings.Trim(url, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	// A remote may be nested (self-hosted GitLab-style paths); owner/name is
	// the last two segments either way.
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

// fetch asks gh for this repo's recent PRs.
//
// Open PRs are not enough: a merged PR is the answer to "did that land", the
// question you have right after the session that opened it goes quiet. `--state
// all` with a small limit covers both without paging through history.
func fetch(slug string) ([]ghPR, error) {
	cmd := exec.Command("gh", "pr", "list",
		"--repo", slug,
		"--state", "all",
		"--limit", "30",
		"--json", "number,title,state,url,headRefName,reviewDecision,updatedAt,statusCheckRollup",
	)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(pollTimeout):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("brd: gh pr list %s: timed out", slug)
	}
	if err != nil {
		return nil, fmt.Errorf("brd: gh pr list %s: %w", slug, err)
	}
	var prs []ghPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("brd: parse gh output for %s: %w", slug, err)
	}
	return prs, nil
}

func toPull(slug string, pr ghPR) store.Pull {
	updated, err := time.Parse(time.RFC3339, pr.UpdatedAt)
	if err != nil {
		updated = time.Now()
	}
	return store.Pull{
		Repo:      slug,
		Number:    pr.Number,
		Ticket:    TicketFrom(pr.HeadRefName, pr.Title),
		Branch:    pr.HeadRefName,
		Title:     pr.Title,
		State:     pr.State,
		Review:    pr.ReviewDecision,
		Checks:    rollup(pr),
		URL:       pr.URL,
		UpdatedAt: updated,
	}
}

// rollup collapses every check on a PR into one word.
//
// A failing check outranks a pending one: the board's job is to surface the
// thing that needs attention, and "still running" is not it. GitHub reports
// two shapes in the same array — CheckRun uses status/conclusion, StatusContext
// uses state — so both are read.
func rollup(pr ghPR) string {
	if len(pr.StatusCheck) == 0 {
		return "none"
	}
	pending := false
	for _, c := range pr.StatusCheck {
		verdict := c.Conclusion
		if verdict == "" {
			verdict = c.State
		}
		switch strings.ToUpper(verdict) {
		case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED":
			return "failing"
		case "SUCCESS", "NEUTRAL", "SKIPPED":
		default:
			pending = true
		}
		if strings.EqualFold(c.Status, "IN_PROGRESS") || strings.EqualFold(c.Status, "QUEUED") {
			pending = true
		}
	}
	if pending {
		return "pending"
	}
	return "passing"
}

// TicketFrom finds the JIRA key for a PR, preferring the branch.
//
// The branch is where the local convention guarantees it (the PR guard refuses
// a PR whose branch lacks one), and it is set before the PR exists. The title
// is the fallback for a PR opened from a branch that predates the convention.
func TicketFrom(branch, title string) string {
	if t := ticketPattern.FindString(branch); t != "" {
		return t
	}
	return ticketPattern.FindString(title)
}

// ticketPattern matches the JIRA keys in use here: CPLAT-11964, DINF-4659.
var ticketPattern = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-\d+\b`)
