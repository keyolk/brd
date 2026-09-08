package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/keyolk/brd/internal/store"
)

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// press runs one key through the real handler and returns the new model, so
// these tests cover the normalization exactly where it sits.
func press(m Model, key string) Model {
	next, _ := m.handleKey(keyMsg(key))
	return next.(Model)
}

func TestNormalizeCJKKeyMapsJamoByPhysicalPosition(t *testing.T) {
	for jamo, want := range map[string]string{
		"ㅂ": "q", "ㅁ": "a", "ㅋ": "z", "ㅓ": "j", "ㅏ": "k",
		"ㅃ": "Q", "ㄲ": "R",
	} {
		if got := normalizeCJKKey(keyMsg(jamo)).String(); got != want {
			t.Errorf("normalizeCJKKey(%q) = %q, want %q", jamo, got, want)
		}
	}
}

func TestNormalizeCJKKeyLeavesEverythingElseAlone(t *testing.T) {
	// Latin keys, digits, and composed syllables pass through: a composed
	// syllable only reaches the TUI as committed text, never as a shortcut.
	for _, k := range []string{"q", "R", "0", "가"} {
		if got := normalizeCJKKey(keyMsg(k)).String(); got != k {
			t.Errorf("normalizeCJKKey(%q) = %q, want it unchanged", k, got)
		}
	}
	for _, in := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyCtrlC}, {Type: tea.KeyEsc}} {
		if got := normalizeCJKKey(in); got.String() != in.String() {
			t.Errorf("normalizeCJKKey(%q) = %q, want it unchanged", in.String(), got.String())
		}
	}
}

func TestNormalizeCJKKeyLeavesPasteAndAltAlone(t *testing.T) {
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'ㅂ'}, Paste: true}
	if got := normalizeCJKKey(paste); got.String() != paste.String() {
		t.Errorf("pasted jamo was rewritten to %q", got.String())
	}
	alt := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'ㅂ'}, Alt: true}
	if got := normalizeCJKKey(alt); got.String() != alt.String() {
		t.Errorf("alt chord was rewritten to %q", got.String())
	}
}

// Under a Korean input source every shortcut arrives as a jamo. They have to
// map back, or the board is unusable until the input source is switched.
func TestHangulShortcutsFireLikeLatin(t *testing.T) {
	t.Run("column navigation", func(t *testing.T) {
		m := Model{}
		// `ㅣ` is the physical `l`, `ㅗ` the physical `h`.
		m = press(m, "ㅣ")
		if m.col != 1 {
			t.Fatalf("col = %d after ㅣ; want 1", m.col)
		}
		m = press(m, "ㅗ")
		if m.col != 0 {
			t.Fatalf("col = %d after ㅗ; want 0", m.col)
		}
	})
	t.Run("row navigation", func(t *testing.T) {
		// Two items in the focused column so j/k have somewhere to go.
		m := Model{items: []store.Item{
			{ID: "a", State: store.StateWaiting},
			{ID: "b", State: store.StateWaiting},
		}}
		if len(m.inColumn(0)) != 2 {
			t.Fatalf("fixture put %d items in the first column, want 2", len(m.inColumn(0)))
		}
		// `ㅓ` is the physical `j`.
		m = press(m, "ㅓ")
		if m.row != 1 {
			t.Fatalf("row = %d after ㅓ; want 1", m.row)
		}
	})
	t.Run("group toggle", func(t *testing.T) {
		// `ㅅ` is the physical `t`, which groups by ticket.
		m := press(Model{}, "ㅅ")
		if !m.group {
			t.Fatal("ㅅ (physical t) did not toggle grouping")
		}
	})
}

// The detail pane closes on q before the app quits, and that layering has to
// survive normalization.
func TestHangulClosesTheDetailPaneBeforeQuitting(t *testing.T) {
	m := Model{detail: true}
	// `ㅂ` is the physical `q`.
	m = press(m, "ㅂ")
	if m.detail {
		t.Fatal("ㅂ (physical q) did not close the detail pane")
	}
}
