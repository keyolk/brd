// Package tui renders the board.
//
// Four columns, and the leftmost is the one that matters: Blocked is the
// answer to "which session is waiting on me", the question that costs the
// most time when a dozen sessions are open. twm made the same call by leading
// with its waiting count, and this board is that idea with the reason
// attached — a blocked item says *what* it is blocked on, not just that it is.
package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/brd/internal/pulls"
	"github.com/keyolk/brd/internal/store"
)

// Column is one board column.
type Column struct {
	Title string
	Hint  string // what this column means, shown when it is empty
}

// Columns is the board layout, left to right in order of how much the user
// needs to look at them.
var Columns = []Column{
	{Title: "Blocked", Hint: "nothing waiting on you"},
	{Title: "Working", Hint: "no turn in flight"},
	{Title: "Background", Hint: "no parked work"},
	{Title: "Idle", Hint: "no session at a prompt"},
}

// refreshInterval is how often the board re-reads the database.
//
// Hooks write on turn boundaries, which are seconds to minutes apart, so a
// faster poll buys nothing. Two seconds keeps a state change visible before
// the user wonders whether the board is stuck.
const refreshInterval = 2 * time.Second

// window is how far back the board looks. A session untouched for a day is
// history, and history belongs in ccx, not on a board of live work.
const window = 24 * time.Hour

type tickMsg time.Time

type loadedMsg struct {
	items []store.Item
	pulls []store.Pull
	err   error
}

// pollMsg carries the outcome of one PR poll.
type pollMsg struct{ err error }

// pollInterval is how often the board asks GitHub about the PRs it tracks.
//
// Far slower than the board's own refresh, because the two answer different
// clocks: hooks land in seconds, while a review lands whenever a person gets
// to it. One minute keeps a merge visible soon after it happens without
// spending a gh call every couple of seconds.
const pollInterval = 60 * time.Second

// Model is the board.
type Model struct {
	db     *store.DB
	items  []store.Item
	err    error
	col    int // focused column
	// sel is the selected item's ID — the source of truth for the selection.
	// Items arrive ordered by updated_at, so any session finishing a turn
	// reorders its column; an index cursor then silently points at a
	// different session, and J/R act on the wrong one. row is only the last
	// position sel resolved to, kept for rendering and as the fallback when
	// sel leaves the column.
	sel    string
	row    int
	w, h   int
	detail bool   // detail pane open for the selected item
	group  bool   // group items by JIRA ticket
	status string // transient feedback for an action that had nothing to say
	// statusLvl decides how the status looks and how long it lives: info
	// expires on a timer, error stays until the next keystroke.
	statusLvl statusLevel
	// statusSeq numbers every status set, so an expiry timer armed for an
	// older message cannot clear a newer one.
	statusSeq int
	pulls  []store.Pull
	// polling guards against stacking gh calls when one runs long: the timer
	// keeps firing regardless of how slow the network is.
	polling bool
}

// New builds the board model.
func New(db *store.DB) Model { return Model{db: db} }

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.load(), tick(), pollTick())
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) load() tea.Cmd {
	return func() tea.Msg {
		items, err := m.db.Items(window)
		if err != nil {
			return loadedMsg{err: err}
		}
		// tmux is asked on every refresh, not cached: it is the only thing
		// that knows whether a session still exists, and at 7ms for every
		// pane it is cheaper than the database read it accompanies.
		items = applyLiveness(items, liveSessions())
		prs, err := m.db.Pulls()
		return loadedMsg{items: items, pulls: prs, err: err}
	}
}

// poll refreshes PR state in the background. It reads the items it was given
// rather than the live model so it cannot race the next refresh.
func (m Model) poll() tea.Cmd {
	items := m.items
	db := m.db
	return func() tea.Msg { return pollMsg{err: pulls.Poll(db, items)} }
}

func pollTick() tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return pollTimerMsg{} })
}

type pollTimerMsg struct{}

type statusLevel int

const (
	statusInfo statusLevel = iota
	statusError
)

// statusTTL is how long an info message stays before the hint bar returns.
// Long enough to read a short sentence, short enough that the keys are back
// before the user goes looking for them.
const statusTTL = 4 * time.Second

// statusExpiredMsg clears an info status, if it is still the one with seq.
type statusExpiredMsg struct{ seq int }

// setStatus shows text at the given level. Info returns the command that
// expires it; error returns nil because it is cleared by the next key —
// a failure should not vanish before the user has seen it.
func (m *Model) setStatus(lvl statusLevel, text string) tea.Cmd {
	m.statusSeq++
	m.status, m.statusLvl = text, lvl
	if lvl != statusInfo {
		return nil
	}
	seq := m.statusSeq
	return tea.Tick(statusTTL, func(time.Time) tea.Msg { return statusExpiredMsg{seq: seq} })
}

// inColumn returns the items in column i, in board order.
func (m Model) inColumn(i int) []store.Item {
	if i < 0 || i >= len(Columns) {
		return nil
	}
	want := Columns[i].Title
	var out []store.Item
	for _, it := range m.items {
		// An ended session has no column. The board is what is in flight;
		// reading finished sessions is a session browser's job, and keeping
		// them here is what made "Done" read as a graveyard of 11 items that
		// were mostly still alive.
		if it.State == store.StateEnded {
			continue
		}
		if it.Column() == want {
			out = append(out, it)
		}
	}
	if m.group {
		// A stable sort, so recency still orders items inside one group —
		// which is the order that makes sense once you are looking at a
		// single ticket or repository.
		sort.SliceStable(out, func(a, b int) bool {
			return out[a].GroupKey() < out[b].GroupKey()
		})
	}
	return out
}

func (m Model) selected() (store.Item, bool) {
	in := m.inColumn(m.col)
	if m.sel != "" {
		for _, it := range in {
			if it.ID == m.sel {
				return it, true
			}
		}
		return store.Item{}, false
	}
	if m.row < 0 || m.row >= len(in) {
		return store.Item{}, false
	}
	return in[m.row], true
}

// resolve re-derives row from sel after the focused column changed under it
// (a refresh, a regroup). If sel is gone from the column the cursor stays at
// the same position, clamped, and sel moves to whatever sits there now.
func (m *Model) resolve() {
	in := m.inColumn(m.col)
	if m.sel != "" {
		for i, it := range in {
			if it.ID == m.sel {
				m.row = i
				return
			}
		}
	}
	m.setRow(m.row)
}

// setRow moves the cursor to a position in the focused column and records
// the item there as the selection.
func (m *Model) setRow(row int) {
	in := m.inColumn(m.col)
	m.row = clampRow(row, len(in))
	if m.row < 0 {
		m.row = 0
	}
	m.sel = ""
	if m.row < len(in) {
		m.sel = in[m.row].ID
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.load(), tick())

	case pollTimerMsg:
		if m.polling || len(m.items) == 0 {
			return m, pollTick()
		}
		m.polling = true
		return m, tea.Batch(m.poll(), pollTick())

	case pollMsg:
		m.polling = false
		if msg.err != nil {
			// A PR poll failing is not a broken board — gh may simply not be
			// logged in — so it goes to the status line, not m.err.
			m.setStatus(statusError, "PR poll: "+msg.err.Error())
		}
		return m, m.load()

	case statusExpiredMsg:
		if msg.seq == m.statusSeq && m.statusLvl == statusInfo {
			m.status = ""
		}
		return m, nil

	case loadedMsg:
		m.items, m.pulls, m.err = msg.items, msg.pulls, msg.err
		// Follow the selected item by ID, not position: a refresh reorders
		// columns by recency, so the same index can name a different session.
		m.resolve()
		// A detail pane whose item left the column has nothing to show, and
		// an open pane reading "nothing selected" is worse than no pane.
		if _, ok := m.selected(); !ok {
			m.detail = false
		}
		return m, nil

	case jumpMsg:
		if msg.err != nil {
			m.setStatus(statusError, msg.err.Error())
		}
		return m, m.load()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Under a Korean input source the shortcut keys arrive as jamo (`q` -> `ㅂ`).
	// Rewrite them to the Latin key at the same physical position so shortcuts
	// fire without switching the input source back. Unconditional: this TUI has
	// no text entry, so no key is ever meant as a literal character.
	msg = normalizeCJKKey(msg)

	// An error has been on screen since it arrived; any keystroke means the
	// user has seen it, so the hint bar comes back. The key still acts.
	if m.status != "" && m.statusLvl == statusError {
		m.status = ""
	}

	switch msg.String() {
	case "q", "ctrl+c":
		if m.detail {
			m.detail = false
			return m, nil
		}
		return m, tea.Quit

	case "esc":
		// esc only closes. It is the key people press to back out of
		// something, and on the bare board that used to quit the app — one
		// reflexive keystroke too many lost the board entirely.
		m.detail = false
		return m, nil

	case "j", "down":
		m.resolve()
		if n := len(m.inColumn(m.col)); n > 0 {
			m.setRow((m.row + 1) % n)
		}
	case "k", "up":
		m.resolve()
		if n := len(m.inColumn(m.col)); n > 0 {
			m.setRow((m.row - 1 + n) % n)
		}
	case "h", "left":
		m.col = (m.col - 1 + len(Columns)) % len(Columns)
		m.setRow(m.row)
	case "l", "right":
		m.col = (m.col + 1) % len(Columns)
		m.setRow(m.row)

	case "b":
		// Jump to the first blocked item — the one keystroke that answers
		// "who is waiting on me" without navigating there.
		m.col, m.detail = 0, false
		m.setRow(0)

	case "enter", " ":
		if _, ok := m.selected(); ok {
			m.detail = !m.detail
		}

	case "t":
		// Group by ticket. Off by default: it only helps once several
		// sessions share one, and a board of ungrouped items reads worse
		// with a header above every single row.
		// The selection survives the reorder: it is the same item, just at a
		// new position.
		m.group = !m.group
		m.resolve()

	case "J":
		// Capital J, so it cannot be hit while navigating with j.
		// No liveness pre-check here: paneFor shells out to tmux, and Update
		// must not block on a subprocess (a wedged tmux server would freeze
		// the board). jump() does the same lookup off the UI goroutine and
		// reports a missing pane through jumpMsg.
		if it, ok := m.selected(); ok {
			m.status = ""
			return m, jump(it.ID)
		}

	case "R":
		if it, ok := m.selected(); ok {
			m.status = ""
			return m, resume(it)
		}

	case "r":
		return m, m.load()
	}
	return m, nil
}

func clampRow(row, n int) int {
	if n == 0 {
		return 0
	}
	if row >= n {
		return n - 1
	}
	return row
}

// ShortAge renders a duration in the least space that stays unambiguous.
func ShortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// RepoName is the last path element of a repo root, which is what the user
// calls it. The full path is in the detail pane for the rare ambiguous case.
func RepoName(repo string) string {
	if repo == "" {
		return "—"
	}
	return filepath.Base(repo)
}

// truncate cuts s to w display columns, on a rune boundary.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > w {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// pullsFor returns the PRs belonging to an item.
//
// A ticket matches first and matches broadly — that is the point of grouping
// by one, since several sessions and several PRs share it. Without a ticket,
// the branch is the only link, and it is exact.
func (m Model) pullsFor(it store.Item) []store.Pull { return PullsFor(it, m.pulls) }

// PullsFor is the item↔PR join, exported so `brd ls` matches the TUI exactly.
func PullsFor(it store.Item, all []store.Pull) []store.Pull {
	var out []store.Pull
	for _, p := range all {
		switch {
		case it.Ticket != "" && p.Ticket == it.Ticket:
		case it.Branch != "" && p.Branch == it.Branch:
		default:
			continue
		}
		out = append(out, p)
	}
	return out
}

// PullBadge is the one-glance summary of an item's PRs.
//
// It shows the PR that most needs the user, not the newest: a failing check on
// an older PR matters more than a green one opened since. Merged PRs are shown
// only when nothing else is open, because "it landed" is the answer you want
// right after a session goes quiet.
func PullBadge(prs []store.Pull) (text string, attention bool) {
	if len(prs) == 0 {
		return "", false
	}
	var open, merged []store.Pull
	for _, p := range prs {
		switch p.State {
		case "OPEN":
			open = append(open, p)
		case "MERGED":
			merged = append(merged, p)
		}
	}
	if len(open) == 0 {
		if len(merged) > 0 {
			return fmt.Sprintf("#%d merged", merged[0].Number), false
		}
		return "", false
	}
	pick := open[0]
	for _, p := range open {
		if p.Attention() {
			pick = p
			break
		}
	}
	label := fmt.Sprintf("#%d", pick.Number)
	switch {
	case pick.Checks == "failing":
		return label + " checks failing", true
	case pick.Review == "CHANGES_REQUESTED":
		return label + " changes requested", true
	case pick.Review == "APPROVED":
		return label + " approved", false
	case pick.Checks == "pending":
		return label + " checks running", false
	}
	return label + " in review", false
}

// sharedGroups reports whether any group covers more than one item — the only
// situation where grouping changes what the board looks like.
//
// This is per-column, because grouping is per-column: two sessions in one
// repository that sit in different columns are not brought together by it, so
// offering the key for them would be a promise the board does not keep.
func (m Model) sharedGroups() bool {
	for i := range Columns {
		seen := map[string]bool{}
		for _, it := range m.inColumn(i) {
			// Ask the item whether it has a group rather than matching the
			// sort-key prefix GroupKey happens to use for "none".
			if it.GroupLabel() == "" {
				continue // nothing to group by
			}
			key := it.GroupKey()
			if seen[key] {
				return true
			}
			seen[key] = true
		}
	}
	return false
}
