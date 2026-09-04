package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/brd/internal/store"
)

// board builds a model with fixed items, bypassing the database. The view is
// a pure function of the model, so rendering needs no store.
func board(w, h int, items ...store.Item) Model {
	m := Model{items: items, w: w, h: h}
	return m
}

func item(id, repo, title, state, blockedOn string, kids ...store.Child) store.Item {
	now := time.Now()
	return store.Item{
		ID: id, Repo: repo, Title: title, Subject: title,
		State: state, BlockedOn: blockedOn, Children: kids,
		CreatedAt: now, UpdatedAt: now,
		StateSince: now.Add(-12 * time.Minute),
	}
}

// The board must never scroll horizontally: a column wider than its share
// pushes the next column off screen, and the rightmost columns vanish.
func TestNoLineExceedsTerminalWidth(t *testing.T) {
	widths := []int{60, 80, 100, 120, 160, 200}
	items := []store.Item{
		item("s1", "/src/keyolk/ghx", "PR 리뷰 코멘트를 반영해줘", store.StateWaiting, "permission_prompt"),
		item("s2", "/src/keyolk/brd", "아주 긴 제목을 넣어서 잘리는지 확인한다 그리고 더 길게 이어서 씁니다 계속",
			store.StateWorking, ""),
		item("s3", "/src/keyolk/tweb", "deploy", store.StateBackground, "",
			store.Child{Kind: "shell", Ref: "t1", Label: "go test ./... -race", State: "running"}),
		item("s4", "/src/keyolk/okx", "watch", store.StateDone, ""),
	}
	for _, w := range widths {
		m := board(w, 24, items...)
		for i, line := range strings.Split(m.View(), "\n") {
			if got := lipglossWidth(line); got > w {
				t.Errorf("width %d: line %d is %d cols wide:\n%s", w, i, got, line)
			}
		}
	}
}

// Four columns squeezed into a narrow pane are unreadable, so a narrow board
// shows the focused column full width instead.
func TestNarrowBoardCollapsesToOneColumn(t *testing.T) {
	it := item("s1", "/src/keyolk/ghx", "blocked thing", store.StateWaiting, "permission_prompt")
	m := board(50, 20, it)
	out := m.View()
	if !strings.Contains(out, "Blocked") {
		t.Errorf("narrow board lost its column title:\n%s", out)
	}
	// The other column titles must be absent — that is what "collapsed" means.
	if strings.Contains(out, "Background") {
		t.Errorf("narrow board rendered more than the focused column:\n%s", out)
	}
	if !strings.Contains(out, "(1/4)") {
		t.Errorf("narrow board must say which column is shown:\n%s", out)
	}
}

// The reason an item is blocked is the actionable half. A card that shows
// only an age says nothing the user can act on.
func TestBlockedCardShowsWhy(t *testing.T) {
	it := item("s1", "/src/keyolk/ghx", "review comments", store.StateWaiting, "permission_prompt")
	out := board(140, 20, it).View()
	if !strings.Contains(out, "permission_prompt") {
		t.Errorf("blocked card omitted its reason:\n%s", out)
	}
	if !strings.Contains(out, "waiting on you") {
		t.Errorf("header omitted the blocked count:\n%s", out)
	}
}

// An empty board must say what each column means rather than showing four
// bare headers, which read as a broken board.
func TestEmptyBoardExplainsItself(t *testing.T) {
	out := board(140, 20).View()
	for _, col := range Columns {
		if !strings.Contains(out, col.Hint) {
			t.Errorf("empty column %q lost its hint %q:\n%s", col.Title, col.Hint, out)
		}
	}
	if !strings.Contains(out, "idle") {
		t.Errorf("empty board header should read idle:\n%s", out)
	}
}

// The hint bar must not advertise keys that would do nothing — that is what
// pushed the useful keys off the end of ghx's bar.
func TestHintsOnlyListLiveKeys(t *testing.T) {
	t.Run("empty board hides navigation", func(t *testing.T) {
		hints := board(140, 20).renderHints()
		if strings.Contains(hints, "hjkl") {
			t.Errorf("empty board offered navigation: %q", hints)
		}
		if !strings.Contains(hints, "q quit") {
			t.Errorf("q must always be offered: %q", hints)
		}
	})

	t.Run("b appears only when blocked items exist elsewhere", func(t *testing.T) {
		working := item("s1", "/src/a", "x", store.StateWorking, "")
		if h := board(140, 20, working).renderHints(); strings.Contains(h, "b blocked") {
			t.Errorf("offered b with nothing blocked: %q", h)
		}

		blocked := item("s2", "/src/b", "y", store.StateWaiting, "idle_prompt")
		m := board(140, 20, working, blocked)
		m.col = 1 // focus Working, so jumping to Blocked is a real move
		if h := m.renderHints(); !strings.Contains(h, "b blocked") {
			t.Errorf("did not offer b while blocked items exist: %q", h)
		}
		m.col = 0 // already in Blocked; the jump is a no-op
		if h := m.renderHints(); strings.Contains(h, "b blocked") {
			t.Errorf("offered b while already in Blocked: %q", h)
		}
	})
}

// A refresh reorders columns under the cursor. Leaving the row index alone
// would silently select a different item, or none.
func TestCursorSurvivesItemsShrinking(t *testing.T) {
	a := item("s1", "/src/a", "a", store.StateWaiting, "idle_prompt")
	b := item("s2", "/src/b", "b", store.StateWaiting, "idle_prompt")
	m := board(140, 20, a, b)
	m.row = 1
	if _, ok := m.selected(); !ok {
		t.Fatal("second row should be selectable")
	}

	// b moves out of Blocked; the cursor must fall back onto a.
	next, _ := m.Update(loadedMsg{items: []store.Item{a}})
	m2 := next.(Model)
	sel, ok := m2.selected()
	if !ok {
		t.Fatalf("cursor left nothing selected, row = %d", m2.row)
	}
	if sel.ID != "s1" {
		t.Errorf("selected %q, want s1", sel.ID)
	}
}

// Navigation must not panic or select out of range on an empty column, which
// is the common case for Blocked.
func TestNavigatingEmptyColumnIsSafe(t *testing.T) {
	m := board(140, 20, item("s1", "/src/a", "a", store.StateDone, ""))
	for _, key := range []string{"l", "l", "j", "k", "enter", "h", "b", "j"} {
		next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		m = next.(Model)
		if m.row < 0 {
			t.Fatalf("key %q produced a negative row", key)
		}
	}
}

func TestChildSummaryCollapsesByKind(t *testing.T) {
	cases := []struct {
		name string
		kids []store.Child
		want string
	}{
		{"single", []store.Child{{Kind: "shell"}}, "shell"},
		{"counted", []store.Child{{Kind: "shell", Ref: "1"}, {Kind: "shell", Ref: "2"}}, "shell×2"},
		{"mixed keeps order", []store.Child{
			{Kind: "subagent", Ref: "a"}, {Kind: "shell", Ref: "1"}, {Kind: "shell", Ref: "2"},
		}, "subagent shell×2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := childSummary(tc.kids); got != tc.want {
				t.Errorf("childSummary = %q, want %q", got, tc.want)
			}
		})
	}
}

// The detail pane is where the children a card summarized get named.
func TestDetailNamesEveryChild(t *testing.T) {
	it := item("s1", "/src/keyolk/brd", "research", store.StateBackground, "",
		store.Child{Kind: "shell", Ref: "t1", Label: "go test ./...", State: "running"},
		store.Child{Kind: "subagent", Ref: "a1", Label: "claude-code-guide", State: "running"},
	)
	m := board(140, 24, it)
	m.col = 2 // Background, where a parked item lives
	m.detail = true
	out := m.View()
	for _, want := range []string{"go test ./...", "claude-code-guide", "in flight (2)"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail omitted %q:\n%s", want, out)
		}
	}
}

func TestShortAge(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{90 * time.Second, "1m"},
		{45 * time.Minute, "45m"},
		{90 * time.Minute, "1h30m"},
		{50 * time.Hour, "2d"},
	}
	for _, tc := range cases {
		if got := ShortAge(tc.in); got != tc.want {
			t.Errorf("ShortAge(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Every state must land in a column, including one this build does not know:
// an unmapped state that rendered nowhere would drop an item off the board
// silently, which is worse than showing it in the wrong place.
func TestEveryStateHasAColumn(t *testing.T) {
	states := []string{
		store.StateWaiting, store.StateWorking, store.StateBackground,
		store.StateDone, store.StateEnded, "some-future-state",
	}
	titles := map[string]bool{}
	for _, c := range Columns {
		titles[c.Title] = true
	}
	for _, s := range states {
		col := store.Item{State: s}.Column()
		if !titles[col] {
			t.Errorf("state %q maps to unknown column %q", s, col)
		}
	}
}

// truncate must not split a multi-byte rune — most titles here are Korean.
func TestTruncateIsRuneSafe(t *testing.T) {
	const s = "칸반 보드를 만들어서 진척도를 보자"
	for w := 1; w <= 40; w++ {
		got := truncate(s, w)
		if lipglossWidth(got) > w {
			t.Errorf("truncate(%d) = %q, %d cols wide", w, got, lipglossWidth(got))
		}
		if !utf8Valid(got) {
			t.Errorf("truncate(%d) produced invalid UTF-8: %q", w, got)
		}
	}
}

// Thin wrappers so the assertions read as intent rather than as library calls.
func lipglossWidth(s string) int { return lipgloss.Width(s) }
func utf8Valid(s string) bool    { return utf8.ValidString(s) }
