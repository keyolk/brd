package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keyolk/brd/internal/store"
	"github.com/keyolk/tuikit/tmux"
)

// paneRow renders one list-panes row in the shape tmux.PaneFormat asks for,
// with the three cc_state options, so the fake answers exactly what a real
// server would.
func paneRow(id, session, state string, since int64, cwd string) string {
	lf := func(v string) string { return fmt.Sprintf("%d:%s", len(v), v) }
	sinceS := ""
	if since > 0 {
		sinceS = fmt.Sprint(since)
	}
	fixed := []string{id, "@1", "$1", "0", "0", "100", "1", "1", "80", "24"}
	free := []string{lf("main"), lf("zsh"), lf(session), lf(state), lf(sinceS), lf(cwd), lf("claude"), lf("")}
	return strings.Join(fixed, "\t") + "\t" + strings.Join(free, "\t") + "\n"
}

func fakeClient(f *tmux.Fake) tmux.Client { return tmux.Client{Runner: f} }

// The pane options are read in one list-panes call, keyed by session, with
// the pane's cwd carried along so an unknown session needs no second call.
func TestLiveSessionsReadsPaneOptions(t *testing.T) {
	fake := tmux.NewFake().On("list-panes",
		paneRow("%1", "s1", "waiting", 1700000000, "/src/a")+
			paneRow("%2", "", "", 0, "/src/shell")+ // a plain shell: no session
			paneRow("%3", "s2", "working", 1700000100, "/src/b"), nil)

	live, err := liveSessions(t.Context(), fakeClient(fake))
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 {
		t.Fatalf("got %d sessions, want 2: %+v", len(live), live)
	}
	want := Liveness{State: "waiting", Since: time.Unix(1700000000, 0), Pane: "%1", Cwd: "/src/a"}
	if live["s1"] != want {
		t.Errorf("s1 = %+v, want %+v", live["s1"], want)
	}
	if live["s2"].State != "working" || live["s2"].Pane != "%3" {
		t.Errorf("s2 = %+v", live["s2"])
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0][0] != "list-panes" || calls[0][1] != "-a" {
		t.Errorf("calls = %v, want one list-panes -a", calls)
	}
}

// A session stamped on two panes resolves to the more recent stamp, the same
// rule cc_state uses when it releases stale windows.
func TestLiveSessionsNewestStampWins(t *testing.T) {
	fake := tmux.NewFake().On("list-panes",
		paneRow("%1", "s1", "done", 1700000500, "/a")+
			paneRow("%2", "s1", "waiting", 1700000000, "/a"), nil)
	live, err := liveSessions(t.Context(), fakeClient(fake))
	if err != nil {
		t.Fatal(err)
	}
	if live["s1"].Pane != "%1" {
		t.Errorf("pane = %s, want %%1 (the newer stamp)", live["s1"].Pane)
	}
}

// nil and empty mean different things and must stay different: nil is "tmux
// told us nothing" and leaves the board alone; empty is "tmux says nothing is
// live" and ends every item.
func TestLiveSessionsNilVersusEmpty(t *testing.T) {
	failed, err := liveSessions(t.Context(), fakeClient(tmux.NewFake().On("list-panes", "", errors.New("no server"))))
	if err == nil || failed != nil {
		t.Errorf("failed call = (%v, %v), want (nil, error)", failed, err)
	}
	outside, err := liveSessions(t.Context(), tmux.New(func(string) string { return "" }))
	if !errors.Is(err, tmux.ErrNotInTmux) || outside != nil {
		t.Errorf("outside tmux = (%v, %v), want (nil, ErrNotInTmux)", outside, err)
	}
	empty, err := liveSessions(t.Context(), fakeClient(tmux.NewFake().On("list-panes", paneRow("%1", "", "", 0, "/x"), nil)))
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("no Claude panes = (%v, %v), want an empty non-nil map", empty, err)
	}

	items := []store.Item{{ID: "s1", State: store.StateWorking}}
	if got := applyLiveness(items, failed); got[0].State != store.StateWorking {
		t.Errorf("nil liveness changed state to %q", got[0].State)
	}
	if got := applyLiveness(items, empty); got[0].State != store.StateEnded {
		t.Errorf("empty liveness left state %q, want ended", got[0].State)
	}
}

// An unknown live session takes its cwd from the listing itself.
func TestUnknownSessionUsesListedCwd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	got := applyLiveness(nil, map[string]Liveness{"s9": {State: "done", Pane: "%9", Cwd: dir}})
	if len(got) != 1 || got[0].Cwd != dir {
		t.Fatalf("got %+v, want cwd %s", got, dir)
	}
}

// The jump focuses the pane holding the session: list, locate, then the
// kit's select-window + select-pane, all by pane ID.
func TestJumpSelectsWindowThenPane(t *testing.T) {
	fake := tmux.NewFake().
		On("list-panes", paneRow("%7", "s1", "waiting", 1700000000, "/a"), nil).
		On("display-message", "$1\t@1\t%1", nil). // Current
		On("display-message", "$1", nil)          // the target's session
	fake.On("select-window", "", nil).On("select-pane", "", nil)

	msg := jump(fakeClient(fake), "s1")().(jumpMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	var got []string
	for _, c := range fake.Calls() {
		if c[0] == "select-window" || c[0] == "select-pane" || c[0] == "switch-client" {
			got = append(got, strings.Join(c, " "))
		}
	}
	want := []string{"select-window -t %7", "select-pane -t %7"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("focus calls = %v, want %v", got, want)
	}
}

// The target is resolved by session ID when the command runs, not when J is
// pressed: a session that moved panes in between must be followed, not left
// behind on a pane ID captured early.
func TestJumpResolvesThePaneAtExecutionTime(t *testing.T) {
	fake := tmux.NewFake()
	cmd := jump(fakeClient(fake), "s1")
	if len(fake.Calls()) != 0 {
		t.Fatal("jump called tmux before the command ran")
	}
	// The session now lives in %42; it did not when the key was pressed.
	fake.On("list-panes", paneRow("%42", "s1", "waiting", 1700000000, "/a"), nil).
		On("display-message", "$1\t@1\t%1", nil).
		On("display-message", "$1", nil).
		On("select-window", "", nil).
		On("select-pane", "", nil)
	if msg := cmd().(jumpMsg); msg.err != nil {
		t.Fatal(msg.err)
	}
	lines := fake.CallLines()
	if last := lines[len(lines)-1]; last != "select-pane -t %42" {
		t.Errorf("last call = %q, want select-pane -t %%42", last)
	}
}

// A session no pane holds, or a board outside tmux, points at resume; a tmux
// failure says what failed instead.
func TestJumpErrors(t *testing.T) {
	noPane := jump(fakeClient(tmux.NewFake().On("list-panes", paneRow("%1", "other", "done", 1, "/a"), nil)), "s1")().(jumpMsg)
	if !errors.Is(noPane.err, errNoPane) {
		t.Errorf("no pane: err = %v, want errNoPane", noPane.err)
	}
	outside := jump(tmux.New(func(string) string { return "" }), "s1")().(jumpMsg)
	if !errors.Is(outside.err, errNoPane) {
		t.Errorf("outside tmux: err = %v, want errNoPane", outside.err)
	}
	broken := jump(fakeClient(tmux.NewFake().On("list-panes", "", errors.New("server exited"))), "s1")().(jumpMsg)
	if broken.err == nil || errors.Is(broken.err, errNoPane) || !strings.Contains(broken.err.Error(), "server exited") {
		t.Errorf("tmux failure: err = %v, want the tmux error", broken.err)
	}
	failedSelect := jump(fakeClient(tmux.NewFake().
		On("list-panes", paneRow("%7", "s1", "done", 1, "/a"), nil).
		On("display-message", "$1\t@1\t%1", nil).
		On("display-message", "$1", nil).
		On("select-window", "", errors.New("can't find window"))), "s1")().(jumpMsg)
	if failedSelect.err == nil || !strings.Contains(failedSelect.err.Error(), "%7") {
		t.Errorf("select failure: err = %v, want it to name the pane", failedSelect.err)
	}
}
