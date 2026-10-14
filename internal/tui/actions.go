package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/brd/internal/store"
	"github.com/keyolk/tuikit/tmux"
)

// A board that only reports is a board you read and then leave. These two
// actions are what turn a blocked item into something you act on without
// hunting for the window it lives in.

// errNoPane is the jump's answer when no pane holds the session — it ended,
// or it never ran here — which is exactly when resume is the right action.
var errNoPane = errors.New("no live tmux pane for this session — R resumes it instead")

// paneFor finds the tmux pane a session is running in.
//
// The lookup needs no bookkeeping of brd's own: the pane already advertises
// which Claude session it holds, in the `@cc_pane_session` option that the
// user's cc_state hook publishes on every turn. Measured on this machine: 24
// of 48 panes carried one. It reads through liveSessions so that a session
// stamped on two panes jumps to the same one the board shows as live.
func paneFor(ctx context.Context, tm tmux.Client, sessionID string) (string, error) {
	if sessionID == "" {
		return "", errNoPane
	}
	live, err := liveSessions(ctx, tm)
	if errors.Is(err, tmux.ErrNotInTmux) {
		return "", errNoPane
	}
	if err != nil {
		return "", err
	}
	l, ok := live[sessionID]
	if !ok {
		return "", errNoPane
	}
	return l.Pane, nil
}

// jumpMsg reports the outcome of a jump so the board can say what happened
// rather than appearing to ignore the keystroke.
type jumpMsg struct {
	err error
}

// jump switches the terminal to the pane running this session.
//
// The pane is resolved from the session ID when the command runs, not when
// the key is pressed: a session can move panes between the two, and a pane ID
// captured early would focus whatever took its place. tmux.Client.Jump then
// selects the window and the pane — select-pane alone leaves the user looking
// at the correct pane in a window they cannot see — and switches the client
// first when the pane lives in another tmux session.
func jump(tm tmux.Client, sessionID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		pane, err := paneFor(ctx, tm, sessionID)
		if err != nil {
			return jumpMsg{err: err}
		}
		if err := tm.Jump(ctx, pane); err != nil {
			return jumpMsg{err: fmt.Errorf("jump to %s: %w", pane, err)}
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
