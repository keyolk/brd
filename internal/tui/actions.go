package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/brd/internal/store"
)

// A board that only reports is a board you read and then leave. These two
// actions are what turn a blocked item into something you act on without
// hunting for the window it lives in.

// paneFor finds the tmux pane a session is running in.
//
// The lookup needs no bookkeeping of brd's own: the pane already advertises
// which Claude session it holds, in the `@cc_pane_session` option that the
// user's cc_state hook publishes on every turn. Measured on this machine: 24
// of 48 panes carried one. An empty result means the session is not live in
// tmux — it ended, or it never ran here — which is exactly when resume is the
// right action instead.
func paneFor(sessionID string) (string, bool) {
	if sessionID == "" || os.Getenv("TMUX") == "" {
		return "", false
	}
	out, err := exec.Command("tmux", "list-panes", "-a", "-F",
		"#{pane_id}\t#{@cc_pane_session}").Output()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		id, sess, ok := strings.Cut(line, "\t")
		if ok && sess == sessionID {
			return id, true
		}
	}
	return "", false
}

// jumpMsg reports the outcome of a jump so the board can say what happened
// rather than appearing to ignore the keystroke.
type jumpMsg struct {
	err error
}

// jump switches the terminal to the pane running this session.
//
// Two tmux calls, not one: select-window brings the right window forward, and
// select-pane then focuses the specific pane inside it. Doing only the second
// leaves the user looking at the correct pane in a window they cannot see.
func jump(sessionID string) tea.Cmd {
	return func() tea.Msg {
		pane, ok := paneFor(sessionID)
		if !ok {
			return jumpMsg{err: fmt.Errorf("no live tmux pane for this session")}
		}
		if err := exec.Command("tmux", "select-window", "-t", pane).Run(); err != nil {
			return jumpMsg{err: fmt.Errorf("select-window: %w", err)}
		}
		if err := exec.Command("tmux", "select-pane", "-t", pane).Run(); err != nil {
			return jumpMsg{err: fmt.Errorf("select-pane: %w", err)}
		}
		return jumpMsg{}
	}
}

// resumeCmd is the command line that reopens an ended session.
//
// It runs in the session's own cwd, because a Claude session is scoped to the
// directory it started in — resuming from elsewhere gives it the wrong project
// context. The session id is passed explicitly rather than relying on
// --continue, which would pick whatever conversation was most recent there.
func resumeCmd(it store.Item) []string {
	return []string{"claude", "--resume", it.ID}
}

// resume replaces the board with the session, in this terminal.
//
// tea.ExecProcess hands the terminal over and restores the board when the
// session exits, so resuming is a round trip rather than a one-way door. The
// alternative — spawning into a new tmux window — was rejected because this
// session's policy blocks creating windows outside its own, and because a
// board that quietly scatters windows is harder to reason about than one that
// visibly hands over the screen it already owns.
func resume(it store.Item) tea.Cmd {
	argv := resumeCmd(it)
	c := exec.Command(argv[0], argv[1:]...)
	if it.Cwd != "" {
		c.Dir = it.Cwd
	}
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return jumpMsg{err: err}
	})
}
