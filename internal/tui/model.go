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
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	{Title: "Done", Hint: "nothing idle"},
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
	err   error
}

// Model is the board.
type Model struct {
	db     *store.DB
	items  []store.Item
	err    error
	col    int // focused column
	row    int // cursor within the focused column
	w, h   int
	detail bool // detail pane open for the selected item
}

// New builds the board model.
func New(db *store.DB) Model { return Model{db: db} }

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.load(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) load() tea.Cmd {
	return func() tea.Msg {
		items, err := m.db.Items(window)
		return loadedMsg{items: items, err: err}
	}
}

// inColumn returns the items in column i, in board order.
func (m Model) inColumn(i int) []store.Item {
	if i < 0 || i >= len(Columns) {
		return nil
	}
	want := Columns[i].Title
	var out []store.Item
	for _, it := range m.items {
		if it.Column() == want {
			out = append(out, it)
		}
	}
	return out
}

func (m Model) selected() (store.Item, bool) {
	in := m.inColumn(m.col)
	if m.row < 0 || m.row >= len(in) {
		return store.Item{}, false
	}
	return in[m.row], true
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.load(), tick())

	case loadedMsg:
		m.items, m.err = msg.items, msg.err
		// The cursor is an index into a list that just changed under it. A
		// session moving from Working to Blocked shortens the column it left,
		// and leaving the row where it was would select a different item —
		// or nothing — without the user having touched a key.
		if n := len(m.inColumn(m.col)); m.row >= n {
			m.row = max(0, n-1)
		}
		// A detail pane whose item left the column has nothing to show, and
		// an open pane reading "nothing selected" is worse than no pane.
		if _, ok := m.selected(); !ok {
			m.detail = false
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		if m.detail {
			m.detail = false
			return m, nil
		}
		return m, tea.Quit

	case "j", "down":
		if n := len(m.inColumn(m.col)); n > 0 {
			m.row = (m.row + 1) % n
		}
	case "k", "up":
		if n := len(m.inColumn(m.col)); n > 0 {
			m.row = (m.row - 1 + n) % n
		}
	case "h", "left":
		m.col = (m.col - 1 + len(Columns)) % len(Columns)
		m.row = clampRow(m.row, len(m.inColumn(m.col)))
	case "l", "right":
		m.col = (m.col + 1) % len(Columns)
		m.row = clampRow(m.row, len(m.inColumn(m.col)))

	case "b":
		// Jump to the first blocked item — the one keystroke that answers
		// "who is waiting on me" without navigating there.
		m.col, m.row, m.detail = 0, 0, false

	case "enter", " ":
		if _, ok := m.selected(); ok {
			m.detail = !m.detail
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
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
