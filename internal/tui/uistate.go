package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// appName names brd's state directory ($XDG_STATE_HOME/brd/ui.json) and its
// ASCII-glyph switch (BRD_ASCII).
const appName = "brd"

// uiState is what survives a restart: how the board was arranged and which
// session the cursor was on. The selection is an ID, so it lands on the same
// session even after the columns reordered while brd was closed; if that
// session is gone, resolve() keeps the slot like any refresh would.
type uiState struct {
	Group    bool   `json:"group"`
	Column   int    `json:"column"`
	Selected string `json:"selected"`
}

func (m Model) uiState() uiState {
	return uiState{Group: m.group, Column: m.col, Selected: m.sel}
}

// restore applies saved state. A column index from a file is clamped: the
// file may predate a layout change, and an out-of-range col panics in View.
func (m *Model) restore(st uiState) {
	m.group, m.sel = st.Group, st.Selected
	m.col = 0
	if st.Column >= 0 && st.Column < len(Columns) {
		m.col = st.Column
	}
	m.saved = m.uiState()
}

// saveDelay coalesces a burst of changes (holding j) into one write. Each
// write runs off the UI loop, but two in flight could finish out of order and
// leave the older state on disk; the debounce makes overlap unlikely and the
// generation drops every tick but the last.
const saveDelay = 500 * time.Millisecond

type saveTickMsg struct{ gen uint64 }

// armSave re-arms the debounce when the state differs from the last one
// armed, and returns nil otherwise.
func (m *Model) armSave() tea.Cmd {
	st := m.uiState()
	if st == m.saved || m.quitting {
		return nil
	}
	m.saved = st
	var gen uint64
	m.saveGen, gen = m.saveGen.Next()
	return tea.Tick(saveDelay, func(time.Time) tea.Msg { return saveTickMsg{gen: gen} })
}

// SaveErr is the error from the final state write at quit, if any. The board
// is gone by then, so the caller prints it.
func (m Model) SaveErr() error { return m.saveErr }
