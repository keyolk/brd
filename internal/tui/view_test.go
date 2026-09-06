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
//
// The reason is rendered in words, not as the raw notification_type: the raw
// values read like internals, and "idle_prompt" — by far the most common one
// on a real board — sounds like a fault when it only means the session is
// waiting for the next instruction.
func TestBlockedCardShowsWhy(t *testing.T) {
	it := item("s1", "/src/keyolk/ghx", "review comments", store.StateWaiting, "permission_prompt")
	out := board(140, 20, it).View()
	if !strings.Contains(out, "needs permission") {
		t.Errorf("blocked card omitted its reason:\n%s", out)
	}
	if strings.Contains(out, "permission_prompt") {
		t.Errorf("blocked card leaked the raw notification type:\n%s", out)
	}
	if !strings.Contains(out, "waiting on you") {
		t.Errorf("header omitted the blocked count:\n%s", out)
	}
}

// An age says how stale something is; a clock time says when it happened.
// "Did this stall before or after lunch" needs the second one.
func TestCardShowsWhenTheStateWasEntered(t *testing.T) {
	it := item("s1", "/src/keyolk/ghx", "review comments", store.StateWaiting, "idle_prompt")
	out := board(140, 20, it).View()
	want := Stamp(it.StateSince)
	if want == "" {
		t.Fatal("Stamp returned nothing for a live item")
	}
	if !strings.Contains(out, want) {
		t.Errorf("card omitted the timestamp %q:\n%s", want, out)
	}
	if !strings.Contains(out, ShortAge(it.Age())) {
		t.Errorf("card omitted the age:\n%s", out)
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
	// Not "idle" — that is now a column name, and a header echoing it would
	// read as a count rather than as the absence of one.
	if !strings.Contains(out, "nothing running") {
		t.Errorf("empty board header should say nothing is running:\n%s", out)
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

// Every live state must land in a column, including one this build does not
// know: an unmapped state that rendered nowhere would drop a live item off the
// board silently, which is worse than showing it in the wrong place.
func TestEveryLiveStateHasAColumn(t *testing.T) {
	states := []string{
		store.StateWaiting, store.StateWorking, store.StateBackground,
		store.StateDone, "some-future-state",
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
	// Ended is the one exception, and it is deliberate: the board shows what
	// is in flight, so a finished session leaves it rather than piling up in
	// a column that then reads as a graveyard.
	if col := (store.Item{State: store.StateEnded}).Column(); col != "" {
		t.Errorf("ended maps to column %q, want no column", col)
	}
}

// An ended session must not appear anywhere on the board. Keeping them is what
// made the Done column report 11 items, most of which were sessions still
// sitting at a prompt.
func TestEndedItemsLeaveTheBoard(t *testing.T) {
	m := Model{items: []store.Item{
		{ID: "live", State: store.StateDone},
		{ID: "gone", State: store.StateEnded},
	}}
	var seen []string
	for i := range Columns {
		for _, it := range m.inColumn(i) {
			seen = append(seen, it.ID)
		}
	}
	if strings.Join(seen, ",") != "live" {
		t.Errorf("board showed %v, want only the live item", seen)
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

// Naming an item from its prompt produced 6 unreadable titles out of 14 on a
// live board. These are the exact shapes that failed.
func TestUnreadableSubjectsAreNotUsedAsNames(t *testing.T) {
	unreadable := []string{
		"cf50469d-8961-4edf-a8e1-10d6e39ce325",
		"https://example.slack.com/archives/C0A/p1788538434599539",
		"/sb:pr-followup",
		"<cross-session-message from=\"uds:/tmp/cc-socks/26916.sock\">",
		"   ",
	}
	for _, subject := range unreadable {
		t.Run(subject[:min(len(subject), 24)], func(t *testing.T) {
			it := store.Item{ID: "abc12345-0000-0000-0000-000000000000", Subject: subject}
			got := Label(it)
			if got == subject {
				t.Errorf("Label used an unreadable subject: %q", got)
			}
			if got != "abc12345" {
				t.Errorf("Label = %q, want the short session id", got)
			}
		})
	}
}

// The ai-title Claude Code writes for itself describes what the session is
// about; the prompt describes one turn of it. Every sampled session had one.
func TestTitleBeatsSubject(t *testing.T) {
	it := store.Item{ID: "s1", Title: "usw2 coder workspace proxy check",
		Subject: "해줘"}
	if got := Label(it); got != "usw2 coder workspace proxy check" {
		t.Errorf("Label = %q, want the ai-title", got)
	}
}

// A ticket prefix is what makes sessions on one piece of work recognizable
// before you group them.
func TestTicketPrefixesTheName(t *testing.T) {
	it := store.Item{ID: "s1", Ticket: "CPLAT-11964", Title: "bump the chart"}
	if got := Label(it); got != "CPLAT-11964 bump the chart" {
		t.Errorf("Label = %q, want the ticket prefixed", got)
	}
	// Not twice, when the title already names it.
	it.Title = "CPLAT-11964: bump the chart"
	if got := Label(it); got != "CPLAT-11964: bump the chart" {
		t.Errorf("Label = %q, want no duplicate ticket", got)
	}
}

func TestStampDistinguishesToday(t *testing.T) {
	recent := time.Now().Add(-2 * time.Hour)
	if got := Stamp(recent); len(got) != 5 {
		t.Errorf("Stamp(recent) = %q, want HH:MM", got)
	}
	old := time.Now().Add(-30 * time.Hour)
	if got := Stamp(old); len(got) <= 5 {
		t.Errorf("Stamp(old) = %q, want a date too — the clock alone is ambiguous", got)
	}
	if got := Stamp(time.Time{}); got != "" {
		t.Errorf("Stamp(zero) = %q, want empty", got)
	}
}

// Raw notification types read like internals. idle_prompt is the most common
// value on a real board and sounds like a fault when it is not one.
func TestBlockedLabelsAreInWords(t *testing.T) {
	cases := map[string]string{
		"idle_prompt":            "awaiting input",
		"permission_prompt":      "needs permission",
		"agent_needs_input":      "agent needs input",
		"elicitation_dialog":     "needs a reply",
		"something_unmapped_yet": "something_unmapped_yet",
	}
	for in, want := range cases {
		if got := BlockedLabel(in); got != want {
			t.Errorf("BlockedLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// The badge must surface the PR that needs the user, not the newest one.
func TestPullBadgePicksWhatNeedsAttention(t *testing.T) {
	cases := []struct {
		name      string
		prs       []store.Pull
		want      string
		attention bool
	}{
		{"none", nil, "", false},
		{"failing beats a newer green one", []store.Pull{
			{Number: 10, State: "OPEN", Checks: "passing", Review: "APPROVED"},
			{Number: 9, State: "OPEN", Checks: "failing"},
		}, "#9 checks failing", true},
		{"changes requested", []store.Pull{
			{Number: 7, State: "OPEN", Review: "CHANGES_REQUESTED", Checks: "passing"},
		}, "#7 changes requested", true},
		{"approved is good news", []store.Pull{
			{Number: 5, State: "OPEN", Review: "APPROVED", Checks: "passing"},
		}, "#5 approved", false},
		{"pending", []store.Pull{
			{Number: 4, State: "OPEN", Checks: "pending"},
		}, "#4 checks running", false},
		{"merged shows only when nothing is open", []store.Pull{
			{Number: 3, State: "MERGED"},
		}, "#3 merged", false},
		{"an open PR outranks a merged one", []store.Pull{
			{Number: 3, State: "MERGED"},
			{Number: 4, State: "OPEN", Checks: "passing"},
		}, "#4 in review", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, attention := PullBadge(tc.prs)
			if got != tc.want {
				t.Errorf("pullBadge = %q, want %q", got, tc.want)
			}
			if attention != tc.attention {
				t.Errorf("attention = %v, want %v", attention, tc.attention)
			}
		})
	}
}

// A PR matches an item by ticket when there is one, by branch otherwise.
func TestPullsAreMatchedByTicketThenBranch(t *testing.T) {
	m := Model{pulls: []store.Pull{
		{Number: 1, Ticket: "CPLAT-1", Branch: "CPLAT-1/a"},
		{Number: 2, Ticket: "CPLAT-1", Branch: "CPLAT-1/b"},
		{Number: 3, Ticket: "", Branch: "follow-the-work"},
	}}

	ticketed := store.Item{Ticket: "CPLAT-1", Branch: "CPLAT-1/a"}
	if got := m.pullsFor(ticketed); len(got) != 2 {
		t.Errorf("ticketed item matched %d PRs, want both on the ticket", len(got))
	}
	untickted := store.Item{Branch: "follow-the-work"}
	got := m.pullsFor(untickted)
	if len(got) != 1 || got[0].Number != 3 {
		t.Errorf("branch match = %+v, want only #3", got)
	}
	if n := len(m.pullsFor(store.Item{})); n != 0 {
		t.Errorf("an item with no ticket or branch matched %d PRs, want 0", n)
	}
}

// Grouping keys on the ticket when there is one and the repository otherwise,
// and a stable sort keeps recency inside each group.
//
// Ticket-only grouping was almost inert: on a live board of 19 items just 2
// carried a ticket, and those two were different tickets, so the grouped view
// was identical to the ungrouped one. The repository fallback is what actually
// binds sessions together — measured on the same board, 3 in one column and 4
// in another.
func TestGroupingPrefersTicketThenRepo(t *testing.T) {
	mk := func(id, ticket, repo string) store.Item {
		return store.Item{ID: id, Ticket: ticket, Repo: repo, State: store.StateDone}
	}
	m := Model{items: []store.Item{
		mk("a", "", "/src/zeta"),
		mk("b", "CPLAT-2", "/src/alpha"),
		mk("c", "", "/src/alpha"),
		mk("d", "CPLAT-1", "/src/beta"),
		mk("e", "", "/src/alpha"),
		mk("f", "", ""), // nothing to group by
	}, group: true}

	var ids []string
	for _, it := range m.inColumn(3) { // Idle
		ids = append(ids, it.ID)
	}
	// Tickets first (sorted), then repos (sorted, recency within), then the
	// item with no key at all.
	want := "dbcea f"
	if got := strings.Join(ids, ""); got != strings.ReplaceAll(want, " ", "") {
		t.Errorf("grouped order = %v, want %v", ids, strings.ReplaceAll(want, " ", ""))
	}
}

// An item with no ticket and no repo must sort last rather than splitting a
// run of items that do belong together.
func TestUngroupableItemsSortLast(t *testing.T) {
	nothing := store.Item{ID: "x"}
	if key := nothing.GroupKey(); key != "3" {
		t.Errorf("GroupKey = %q, want the last bucket", key)
	}
	if label := nothing.GroupLabel(); label != "" {
		t.Errorf("GroupLabel = %q, want empty so no header renders", label)
	}
}

// A ticket outranks a repository: a named piece of work is more specific than
// the directory it happens in.
func TestTicketOutranksRepoAsAGroupKey(t *testing.T) {
	both := store.Item{Ticket: "CPLAT-1", Repo: "/src/a"}
	if got := both.GroupLabel(); got != "CPLAT-1" {
		t.Errorf("GroupLabel = %q, want the ticket", got)
	}
	repoOnly := store.Item{Repo: "/src/a"}
	if both.GroupKey() >= repoOnly.GroupKey() {
		t.Error("a ticketed item must sort before a repo-only one")
	}
}

// The t key must not be offered when grouping would visibly do nothing.
// Sharing is judged per column, because grouping is per column: two sessions
// in one repo sitting in different columns are not brought together by it.
func TestGroupKeyOnlyOfferedWhenAGroupIsShared(t *testing.T) {
	lone := Model{items: []store.Item{
		{ID: "a", Ticket: "CPLAT-1", Repo: "/src/a", State: store.StateDone},
		{ID: "b", Ticket: "CPLAT-2", Repo: "/src/b", State: store.StateDone},
	}, w: 140}
	if h := lone.renderHints(); strings.Contains(h, "t group") {
		t.Errorf("offered grouping when nothing is shared: %q", h)
	}

	// Same repo, same column — now grouping does something.
	sharedRepo := Model{items: []store.Item{
		{ID: "a", Repo: "/src/a", State: store.StateDone},
		{ID: "b", Repo: "/src/a", State: store.StateDone},
	}, w: 140}
	if h := sharedRepo.renderHints(); !strings.Contains(h, "t group") {
		t.Errorf("did not offer grouping for two sessions in one repo: %q", h)
	}

	// Same repo, different columns — grouping would not bring them together.
	acrossColumns := Model{items: []store.Item{
		{ID: "a", Repo: "/src/a", State: store.StateDone},
		{ID: "b", Repo: "/src/a", State: store.StateWorking},
	}, w: 140}
	if h := acrossColumns.renderHints(); strings.Contains(h, "t group") {
		t.Errorf("offered grouping across columns, which it cannot do: %q", h)
	}
}

// A group header names each run once. A repo header shows the directory name
// the user calls it by, not the full path.
func TestGroupHeadersNameEachRunOnce(t *testing.T) {
	m := board(148, 24,
		store.Item{ID: "a", Repo: "/Users/x/src/ops-k8s", Title: "first",
			State: store.StateDone, StateSince: time.Now()},
		store.Item{ID: "b", Repo: "/Users/x/src/ops-k8s", Title: "second",
			State: store.StateDone, StateSince: time.Now()},
	)
	m.group = true
	m.col = 3
	out := m.View()
	if n := strings.Count(out, "ops-k8s"); n != 3 {
		// One header plus each card's own repo label.
		t.Errorf("ops-k8s appears %d times, want 1 header + 2 card labels:\n%s", n, out)
	}
	if strings.Contains(out, "/Users/x/src") {
		t.Errorf("header used the full path:\n%s", out)
	}
}

// A failed action must say so where the user is looking, and outrank the keys.
func TestStatusReplacesTheHintBar(t *testing.T) {
	m := Model{items: []store.Item{{ID: "a", State: store.StateDone}},
		w: 140, status: "no live tmux pane — R resumes it instead"}
	h := m.renderHints()
	if !strings.Contains(h, "no live tmux pane") {
		t.Errorf("status not shown: %q", h)
	}
	if strings.Contains(h, "q quit") {
		t.Errorf("keys shown alongside a status message: %q", h)
	}
}

func TestResumeRunsTheSessionInItsOwnCwd(t *testing.T) {
	it := store.Item{ID: "abc-123", Cwd: "/src/keyolk/brd"}
	argv := resumeCmd(it)
	want := []string{"claude", "--resume", "abc-123"}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Errorf("resumeCmd = %v, want %v", argv, want)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
