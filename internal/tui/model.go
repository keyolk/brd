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
	"github.com/keyolk/brd/internal/pulls"
	"github.com/keyolk/brd/internal/store"
	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/prefs"
	"github.com/keyolk/tuikit/theme"
	"github.com/keyolk/tuikit/toast"
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
	err   error // items could not be read; nothing in this message is usable
	// pullsErr means the items are fresh but the PR table was not readable.
	pullsErr error
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
	db    *store.DB
	items []store.Item
	// loadFailed records that the last refresh failed, so the next good one
	// can say the board is current again.
	loadFailed bool
	col        int // focused column
	// sel is the selected item's ID — the source of truth for the selection.
	// Items arrive ordered by updated_at, so any session finishing a turn
	// reorders its column; an index cursor then silently points at a
	// different session, and J/R act on the wrong one. row is only the last
	// position sel resolved to, kept for rendering and as the fallback when
	// sel leaves the column.
	sel string
	row int
	// scroll is the first visible item per column. It is kept across frames
	// so moving the cursor up from the bottom of a long column scrolls one
	// card at a time instead of snapping back to the top.
	scroll []int
	w, h   int
	detail bool // detail pane open for the selected item
	group  bool // group items by JIRA ticket
	// toasts carry the outcome of actions and background work. Info expires
	// on its own tick; an error stays until a key after it has been seen.
	toasts toast.Queue
	// th styles the hint bar, toasts and help, and picks ASCII key names and
	// toast prefixes for terminals without ✓/✗. The zero Theme renders plain.
	th    theme.Theme
	pulls []store.Pull
	// polling guards against stacking gh calls when one runs long: the timer
	// keeps firing regardless of how slow the network is.
	polling bool

	km      keys.Keymap // zero means the defaults; see keymap()
	help    bool        // help overlay open
	helpOff int         // help scroll offset, clamped by keys.RenderHelp

	// saved is the UI state last handed to saver, so an unchanged state is
	// not re-armed on every message.
	saved uiState
	saver prefs.Saver[uiState]
	// quitting waits for the final state write before tea.Quit, and saveErr
	// keeps that write's failure for the caller once the screen is gone.
	quitting bool
	saveErr  error
}

// New builds the board model, with the user's key overrides and the UI state
// the last run left behind. Neither failing stops the board: the defaults are
// used and an error toast says what was ignored.
func New(db *store.DB) Model {
	m := Model{db: db, th: newTheme(), saver: prefs.NewSaver[uiState](appName, 0)}
	km, err := baseKeymap.LoadOverrides(keys.ConfigPath(appName), stacks...)
	m.km = km
	if err != nil {
		m.toasts, _ = toast.Push(m.toasts, toast.Toast{Source: "keys", Text: err.Error(), Level: toast.Error})
	}
	st, err := prefs.Load(appName, uiState{})
	if err != nil {
		m.toasts, _ = toast.Push(m.toasts, toast.Toast{Source: "prefs", Text: "UI state ignored: " + err.Error(), Level: toast.Error})
	}
	m.restore(st)
	return m
}

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
		return loadedMsg{items: items, pulls: prs, pullsErr: err}
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
	next, cmd := m.update(msg)
	nm, ok := next.(Model)
	if !ok {
		return next, cmd
	}
	// Any message can move the cursor or change what a column holds, so the
	// scroll offsets are re-fitted once here rather than at every call site.
	nm.syncScroll()
	// Likewise any message can change the state worth keeping (a refresh
	// moves sel when its item leaves the column), so the save is armed here.
	if save := nm.armSave(); save != nil {
		cmd = tea.Batch(cmd, save)
	}
	return nm, cmd
}

// notify queues a toast and returns the tick that expires it.
func (m *Model) notify(source string, lvl toast.Level, text string) tea.Cmd {
	var cmd tea.Cmd
	m.toasts, cmd = toast.Push(m.toasts, toast.Toast{Source: source, Text: text, Level: lvl})
	return cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		var cmd tea.Cmd
		if msg.err != nil {
			// A PR poll failing is not a broken board — gh may simply not be
			// logged in — so it goes to a toast.
			cmd = m.notify("poll", toast.Error, "PR poll: "+msg.err.Error())
		}
		return m, tea.Batch(cmd, m.load())

	case toast.Expired:
		m.toasts = toast.Update(m.toasts, msg)
		return m, nil

	case prefs.SaveTick:
		var cmd tea.Cmd
		m.saver, cmd = m.saver.Update(msg)
		return m, cmd

	case prefs.Saved:
		if msg.Final {
			m.saveErr = msg.Err
			return m, tea.Quit
		}
		if msg.Err != nil {
			return m, m.notify("prefs", toast.Error, "saving UI state: "+msg.Err.Error())
		}
		return m, nil

	case loadedMsg:
		// A failed read keeps the last good board and says so in a toast.
		// Blanking the board for a transient error (a locked database
		// mid-write) flashed an error screen for one tick and threw away
		// everything the user was looking at, cursor included.
		if msg.err != nil {
			m.loadFailed = true
			return m, m.notify("load", toast.Error, "refresh failed, showing last good board: "+msg.err.Error())
		}
		var cmds []tea.Cmd
		if m.loadFailed {
			// Same source as the failure, so this replaces the error.
			m.loadFailed = false
			cmds = append(cmds, m.notify("load", toast.Info, "board refreshed"))
		}
		m.items = msg.items
		if msg.pullsErr != nil {
			cmds = append(cmds, m.notify("pulls", toast.Error, "reading PRs: "+msg.pullsErr.Error()))
		} else {
			m.pulls = msg.pulls
		}
		// Follow the selected item by ID, not position: a refresh reorders
		// columns by recency, so the same index can name a different session.
		m.resolve()
		// A detail pane whose item left the column has nothing to show, and
		// an open pane reading "nothing selected" is worse than no pane.
		if _, ok := m.selected(); !ok {
			m.detail = false
		}
		return m, tea.Batch(cmds...)

	case jumpMsg:
		var cmd tea.Cmd
		if msg.err != nil {
			cmd = m.notify("jump", toast.Error, msg.err.Error())
		}
		return m, tea.Batch(cmd, m.load())

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// An error has been on screen since it arrived; a keystroke after it was
	// seen means the user has read it, so the hint bar comes back. The key
	// still acts.
	m.toasts = toast.OnKey(m.toasts)
	if m.quitting {
		return m, nil // the final save is in flight; quit follows it
	}
	b, ok := m.keymap().Resolve(m.active(), msg, m.keyOpts())
	if !ok {
		return m, nil
	}
	return m.run(b.Action)
}

// run performs one named action from the binding table.
func (m Model) run(action string) (tea.Model, tea.Cmd) {
	switch action {
	case actQuit:
		// Write the final state, then quit when the write reports back: a
		// debounce still pending would otherwise die with the program.
		m.quitting = true
		m.saver, _ = m.saver.Save(m.uiState()) // the debounce tick is dropped: Flush supersedes it
		var cmd tea.Cmd
		m.saver, cmd = m.saver.Flush()
		return m, cmd

	case actDetailClose:
		// esc and q back out of the pane. On the bare board esc is unbound:
		// it used to quit, and one reflexive keystroke too many lost the
		// board entirely.
		m.detail = false

	case actHelp:
		if m.help {
			return m.run(actHelpClose)
		}
		m.help, m.helpOff = true, 0
		m.toasts = toast.Hold(m.toasts) // toasts wait while the overlay is up
	case actHelpClose:
		m.help = false
		var cmd tea.Cmd
		m.toasts, cmd = toast.Release(m.toasts)
		return m, cmd
	case actHelpDown:
		_, m.helpOff = m.helpLines(m.helpOff + 1)
	case actHelpUp:
		_, m.helpOff = m.helpLines(m.helpOff - 1)

	case actDown:
		m.resolve()
		if n := len(m.inColumn(m.col)); n > 0 {
			m.setRow((m.row + 1) % n)
		}
	case actUp:
		m.resolve()
		if n := len(m.inColumn(m.col)); n > 0 {
			m.setRow((m.row - 1 + n) % n)
		}
	case actPrevCol:
		m.col = (m.col - 1 + len(Columns)) % len(Columns)
		m.setRow(m.row)
	case actNextCol:
		m.col = (m.col + 1) % len(Columns)
		m.setRow(m.row)

	case actBlocked:
		// Jump to the first blocked item — the one keystroke that answers
		// "who is waiting on me" without navigating there.
		m.col, m.detail = 0, false
		m.setRow(0)

	case actDetail:
		if _, ok := m.selected(); ok {
			m.detail = !m.detail
		}

	case actGroup:
		// Group by ticket. Off by default: it only helps once several
		// sessions share one, and a board of ungrouped items reads worse
		// with a header above every single row.
		// The selection survives the reorder: it is the same item, just at a
		// new position.
		m.group = !m.group
		m.resolve()

	case actJump:
		// Capital J (or g), so it cannot be hit while navigating with j.
		// No liveness pre-check here: paneFor shells out to tmux, and Update
		// must not block on a subprocess (a wedged tmux server would freeze
		// the board). jump() does the same lookup off the UI goroutine and
		// reports a missing pane through jumpMsg.
		if it, ok := m.selected(); ok {
			return m, jump(it.ID)
		}

	case actResume:
		if it, ok := m.selected(); ok {
			return m, resume(it)
		}

	case actRefresh:
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
