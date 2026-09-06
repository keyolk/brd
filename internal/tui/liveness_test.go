package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keyolk/brd/internal/store"
)

func liveAt(state string, since time.Duration) Liveness {
	return Liveness{State: state, Since: time.Now().Add(-since), Pane: "%1"}
}

// The failure that started this: `Stop` fires when a turn ends, so a session
// sitting at a prompt was recorded as done and read as finished work.
// Measured against tmux, 10 of 26 live sessions were shown that way.
func TestLiveSessionsAreNotShownAsEnded(t *testing.T) {
	items := []store.Item{{ID: "s1", State: store.StateEnded}}
	live := map[string]Liveness{"s1": liveAt("waiting", time.Minute)}

	got := applyLiveness(items, live)[0]
	if !got.Live {
		t.Error("a session held by a live pane was not marked live")
	}
	if got.State == store.StateEnded {
		t.Error("state stayed ended for a session that is still running")
	}
	if got.Column() != "Idle" {
		t.Errorf("column = %q, want Idle", got.Column())
	}
}

// A session that vanished without firing SessionEnd — SIGKILL, a closed
// terminal — must leave the board rather than sitting at its last state
// forever.
func TestSessionsWithNoLivePaneAreEnded(t *testing.T) {
	items := []store.Item{
		{ID: "gone", State: store.StateWorking},
		{ID: "alsogone", State: store.StateWaiting, BlockedOn: "permission_prompt"},
	}
	for _, got := range applyLiveness(items, map[string]Liveness{}) {
		if got.Live {
			t.Errorf("%s marked live though no pane holds it", got.ID)
		}
		if got.State != store.StateEnded {
			t.Errorf("%s state = %q, want %q", got.ID, got.State, store.StateEnded)
		}
	}
}

// Hooks are the authority on *why* an item is where it is; the pane options
// are the authority on *whether it exists*. A more specific board state must
// survive a vaguer live one.
func TestBoardStateSurvivesWhenItSaysMore(t *testing.T) {
	cases := []struct {
		name  string
		item  store.Item
		live  Liveness
		want  string
		keeps string // a field that must not be lost
	}{
		{
			name:  "blocked outranks waiting-at-a-prompt",
			item:  store.Item{ID: "s", State: store.StateWaiting, BlockedOn: "permission_prompt"},
			live:  liveAt("waiting", time.Minute),
			want:  store.StateWaiting,
			keeps: "permission_prompt",
		},
		{
			name: "background outranks mid-turn",
			item: store.Item{ID: "s", State: store.StateBackground},
			live: liveAt("working", time.Minute),
			want: store.StateBackground,
		},
		{
			name: "background outranks at-a-prompt",
			item: store.Item{ID: "s", State: store.StateBackground},
			live: liveAt("waiting", time.Minute),
			want: store.StateBackground,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyLiveness([]store.Item{tc.item},
				map[string]Liveness{"s": tc.live})[0]
			if got.State != tc.want {
				t.Errorf("state = %q, want %q", got.State, tc.want)
			}
			if !got.Live {
				t.Error("not marked live")
			}
			if tc.keeps != "" && got.BlockedOn != tc.keeps {
				t.Errorf("lost the reason: BlockedOn = %q, want %q", got.BlockedOn, tc.keeps)
			}
		})
	}
}

// A pane sees a turn start before any hook reports it, so an item the board
// still calls idle must move to Working.
func TestLiveStatePromotesAnIdleItemToWorking(t *testing.T) {
	items := []store.Item{{ID: "s", State: store.StateDone}}
	got := applyLiveness(items, map[string]Liveness{"s": liveAt("working", 5*time.Second)})[0]
	if got.State != store.StateWorking {
		t.Errorf("state = %q, want %q", got.State, store.StateWorking)
	}
}

// A nil map means there was no signal at all — brd running outside tmux, or a
// dead server. That must not be read as "no session is alive", which would
// empty the board.
func TestNoSignalLeavesTheBoardAlone(t *testing.T) {
	items := []store.Item{
		{ID: "a", State: store.StateWorking},
		{ID: "b", State: store.StateWaiting, BlockedOn: "idle_prompt"},
	}
	got := applyLiveness(items, nil)
	if len(got) != len(items) {
		t.Fatalf("got %d items, want %d", len(got), len(items))
	}
	for i, it := range got {
		if it.State != items[i].State {
			t.Errorf("%s state changed to %q with no signal", it.ID, it.State)
		}
	}
}

// An empty map is different: the query answered, and the answer is that
// nothing is live.
func TestEmptyAnswerEndsEverything(t *testing.T) {
	items := []store.Item{{ID: "a", State: store.StateWorking}}
	got := applyLiveness(items, map[string]Liveness{})[0]
	if got.State != store.StateEnded {
		t.Errorf("state = %q, want %q", got.State, store.StateEnded)
	}
}

// The header's live count must reflect the machine, not the columns. Counting
// only Working and Background reported "3 live" while 26 sessions were up.
func TestHeaderCountsEveryLiveSession(t *testing.T) {
	m := Model{w: 140, items: []store.Item{
		{ID: "a", State: store.StateWaiting, BlockedOn: "permission_prompt", Live: true},
		{ID: "b", State: store.StateDone, Live: true},
		{ID: "c", State: store.StateDone, Live: true},
		{ID: "d", State: store.StateWorking, Live: true},
		{ID: "e", State: store.StateEnded, Live: false},
	}}
	h := m.renderHeader()
	if !strings.Contains(h, "4 live") {
		t.Errorf("header = %q, want 4 live", h)
	}
	if !strings.Contains(h, "1 working") {
		t.Errorf("header = %q, want 1 working", h)
	}
	if !strings.Contains(h, "1 waiting on you") {
		t.Errorf("header = %q, want the blocked count", h)
	}
}

// Live and dead items must be distinguishable on the card itself — the board
// gave no sign at all before, which is what made a mostly-live board read as
// a list of finished work.
func TestCardsMarkLiveSessions(t *testing.T) {
	liveItem := item("s1", "/src/a", "running thing", store.StateDone, "")
	liveItem.Live = true
	deadItem := item("s2", "/src/b", "stopped thing", store.StateDone, "")

	m := board(140, 20, liveItem, deadItem)
	m.col = 3 // Idle
	out := m.View()
	if !strings.Contains(out, "●") {
		t.Errorf("no live marker on the board:\n%s", out)
	}
	for _, want := range []string{"running thing", "stopped thing"} {
		if !strings.Contains(out, want) {
			t.Errorf("card %q missing:\n%s", want, out)
		}
	}
}

// A session that moved panes leaves its options behind on the old one, so the
// most recent stamp has to win or the board follows a stale pane.
func TestMostRecentStampWins(t *testing.T) {
	older := Liveness{State: "done", Since: time.Now().Add(-4 * time.Hour), Pane: "%1"}
	newer := Liveness{State: "working", Since: time.Now().Add(-time.Minute), Pane: "%2"}
	if !newer.Since.After(older.Since) {
		t.Fatal("fixture is wrong")
	}
	got := applyLiveness([]store.Item{{ID: "s", State: store.StateDone}},
		map[string]Liveness{"s": newer})[0]
	if got.State != store.StateWorking {
		t.Errorf("state = %q, want the newer pane's state", got.State)
	}
}

// A session running since before the hooks were installed has fired none of
// them, so the board has no row for it. tmux does, and showing it nameless is
// better than pretending it does not exist. Measured on this machine right
// after install: 8 of 26 live sessions were in exactly this state.
func TestUnknownLiveSessionsAreAdded(t *testing.T) {
	items := []store.Item{{ID: "known", State: store.StateWorking}}
	live := map[string]Liveness{
		"known":   liveAt("working", time.Minute),
		"unknown": liveAt("waiting", 5*time.Minute),
	}
	got := applyLiveness(items, live)
	if len(got) != 2 {
		t.Fatalf("got %d items, want the unknown session added too", len(got))
	}
	var added *store.Item
	for i := range got {
		if got[i].ID == "unknown" {
			added = &got[i]
		}
	}
	if added == nil {
		t.Fatal("the unknown live session was not added")
	}
	if !added.Live {
		t.Error("added item is not marked live")
	}
	if added.Column() != "Idle" {
		t.Errorf("column = %q, want Idle", added.Column())
	}
	if added.StateSince.IsZero() {
		t.Error("added item has no timestamp; tmux supplied one")
	}
}

// A mid-turn session the board has never seen belongs in Working, not Idle.
func TestUnknownWorkingSessionLandsInWorking(t *testing.T) {
	got := applyLiveness(nil, map[string]Liveness{
		"fresh": liveAt("working", 10*time.Second),
	})
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1", len(got))
	}
	if got[0].Column() != "Working" {
		t.Errorf("column = %q, want Working", got[0].Column())
	}
}

// Adding unknown sessions must not duplicate the ones already recorded.
func TestKnownSessionsAreNotDuplicated(t *testing.T) {
	items := []store.Item{
		{ID: "a", State: store.StateWorking},
		{ID: "b", State: store.StateWaiting, BlockedOn: "permission_prompt"},
	}
	live := map[string]Liveness{
		"a": liveAt("working", time.Minute),
		"b": liveAt("waiting", time.Minute),
	}
	if got := applyLiveness(items, live); len(got) != 2 {
		t.Errorf("got %d items, want 2 — no duplicates", len(got))
	}
}
