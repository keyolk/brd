package tui

import (
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

// armSave hands the state to the debounced saver when it differs from the
// last one handed over, and returns nil otherwise. The saver coalesces a burst
// (holding j) into one write, off the UI loop, and orders writes so an older
// state never lands after a newer one.
func (m *Model) armSave() tea.Cmd {
	st := m.uiState()
	if st == m.saved || m.quitting {
		return nil
	}
	m.saved = st
	var cmd tea.Cmd
	m.saver, cmd = m.saver.Save(st)
	return cmd
}

// SaveErr is the error from the final state write at quit, if any. The board
// is gone by then, so the caller prints it.
func (m Model) SaveErr() error { return m.saveErr }
