package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/keyolk/brd/internal/store"
	"github.com/keyolk/brd/internal/transcript"
)

// Liveness is what tmux knows about a session right now.
//
// The board needs this because hooks cannot answer "is it still there". A hook
// reports a transition and then stops talking: `Stop` says the *turn* ended,
// not the session, and a session killed with SIGKILL or a closed terminal
// never fires SessionEnd at all. So the last hook a session fired stays on the
// board forever, whether or not the session outlived it.
//
// Measured on this machine, that is not a corner case: of 26 sessions tmux
// held live, the board disagreed about 16 — ten it called `done` or `ended`
// that were sitting at a prompt, five it had never heard of, and one it called
// `waiting` that had finished.
//
// tmux is the authority here because the user's cc_state hook publishes the
// answer on the pane itself, refreshed every turn, and reading every pane
// costs 7ms.
type Liveness struct {
	State string    // working | waiting | done — cc_state's own vocabulary
	Since time.Time // when that state was entered
	Pane  string
}

// liveSessions reads the state of every Claude session tmux is holding.
//
// A nil result means "tmux told us nothing" — not running under tmux, or the
// server is gone. That is deliberately different from an empty map, which
// would mean "tmux says no sessions are live"; treating the two alike would
// mark every item dead the moment brd ran outside tmux.
func LiveSessions() map[string]Liveness { return liveSessions() }

// ApplyLiveness reconciles items against tmux. Exported so `brd ls` gets the
// same answer the TUI does — a text board that disagreed with the interactive
// one about what is running would be worse than no text board.
func ApplyLiveness(items []store.Item) []store.Item {
	return applyLiveness(items, liveSessions())
}

func liveSessions() map[string]Liveness {
	if os.Getenv("TMUX") == "" {
		return nil
	}
	out, err := exec.Command("tmux", "list-panes", "-a", "-F",
		"#{pane_id}\t#{@cc_pane_session}\t#{@cc_pane_state}\t#{@cc_pane_since}").Output()
	if err != nil {
		return nil
	}
	live := map[string]Liveness{}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 4 || parts[1] == "" {
			continue
		}
		l := Liveness{State: parts[2], Pane: parts[0]}
		if secs, err := strconv.ParseInt(parts[3], 10, 64); err == nil && secs > 0 {
			l.Since = time.Unix(secs, 0)
		}
		// A pane can outlive the session that set these options, and a session
		// can move between panes. The most recent stamp wins, matching what
		// cc_state itself does when it releases stale windows.
		if prev, ok := live[parts[1]]; ok && prev.Since.After(l.Since) {
			continue
		}
		live[parts[1]] = l
	}
	return live
}

// applyLiveness reconciles the board against tmux.
//
// Hooks own *why* an item is in a state — which permission it waits on, what
// background work parked it. tmux owns *whether the session still exists*.
// Where they disagree about existence, tmux wins, because a hook can only
// report the past while tmux is reporting the present.
func applyLiveness(items []store.Item, live map[string]Liveness) []store.Item {
	if live == nil {
		return items // no signal; the board's own state is all there is
	}
	out := make([]store.Item, 0, len(items))
	for _, it := range items {
		l, ok := live[it.ID]
		switch {
		case !ok:
			// tmux does not hold this session. It ended — cleanly, or by
			// being killed without firing SessionEnd.
			it.Live = false
			if it.State != store.StateEnded {
				it.State = store.StateEnded
			}
		case l.State == "working":
			it.Live = true
			// A session mid-turn is working even if its last hook was a Stop:
			// the next UserPromptSubmit has not landed yet.
			if it.State != store.StateWorking && it.State != store.StateBackground {
				it.State = store.StateWorking
				it.StateSince = laterOf(l.Since, it.StateSince)
			}
		case l.State == "waiting", l.State == "done":
			it.Live = true
			// Both mean the turn ended and the session is at a prompt. The
			// board's own state is kept when it is more specific — Background
			// (its own work is still running) and Waiting (we know *what* it
			// waits on) both say more than "at a prompt" does.
			if it.State == store.StateEnded {
				it.State = store.StateDone
				it.StateSince = laterOf(l.Since, it.StateSince)
			}
		}
		out = append(out, it)
	}

	// A live session the board has never recorded still belongs on it. This is
	// the common case right after installing the hooks — a session running
	// since before then has fired none of them — and measured on this machine
	// it was 8 of 26. Showing them nameless is better than pretending they do
	// not exist; the first hook they fire fills in the rest.
	known := make(map[string]bool, len(items))
	for _, it := range items {
		known[it.ID] = true
	}
	for id, l := range live {
		if known[id] {
			continue
		}
		state := store.StateDone
		if l.State == "working" {
			state = store.StateWorking
		}
		cwd := paneCwd(l.Pane)
		it := store.Item{
			ID: id, State: state, Live: true,
			StateSince: l.Since, UpdatedAt: l.Since,
			Cwd: cwd, Repo: repoRoot(cwd),
		}
		// The session's own transcript names it, so an item that arrived from
		// tmux need not read as a bare uuid. Without this, 16 of the board's
		// Idle rows were 8 hex characters and a repo.
		if facts := transcript.Read(transcriptFor(id)); facts.Title != "" {
			it.Title = facts.Title
			it.Branch = facts.Branch
			it.Ticket = transcript.TicketFrom(facts.Branch)
		}
		out = append(out, it)
	}
	return out
}

// paneCwd asks tmux where a pane is, so a session brd has never recorded can
// still be labelled by repository.
//
// Without it such an item shows a bare id and nothing else. The pane's cwd is
// not perfect — the shell may have moved since — but it is what the board has,
// and it is right for the overwhelmingly common case of a pane that stayed
// where it started.
func paneCwd(pane string) string {
	if pane == "" {
		return ""
	}
	out, err := exec.Command("tmux", "display-message", "-p", "-t", pane,
		"#{pane_current_path}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// laterOf prefers whichever timestamp is more recent and non-zero. tmux's
// stamp is usually the fresher one, but a session that moved panes can carry
// an older one than the board's.
func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	if b.IsZero() {
		return a
	}
	return b
}

// repoRoot resolves a directory to its git root, so an item that arrived from
// tmux rather than a hook still groups under the repository it is in.
//
// This duplicates what the hook package does, deliberately: importing it here
// would pull the whole write path into the renderer for one four-line helper.
func repoRoot(cwd string) string {
	if cwd == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return filepath.Clean(cwd)
	}
	if root := strings.TrimSpace(string(out)); root != "" {
		return root
	}
	return filepath.Clean(cwd)
}

// transcriptFor locates a session's transcript by id.
//
// The hook payload hands brd this path directly, but an item that arrived from
// tmux has no payload, so the file has to be found. Sessions live under
// ~/.claude/projects/<sanitized-cwd>/<id>.jsonl and the cwd component cannot
// be derived reliably from the pane, so this globs on the id — measured at
// 23ms, which is acceptable once per unknown session per refresh.
func transcriptFor(sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(
		home, ".claude", "projects", "*", sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}
