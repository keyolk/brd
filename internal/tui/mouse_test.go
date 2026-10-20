package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/brd/internal/store"
)

// at finds where View drew text, so a click lands on the cell the user sees
// rather than on a coordinate the test computed with the same arithmetic as
// the code under test.
func at(t *testing.T, m Model, text string) (x, y int) {
	t.Helper()
	for i, line := range strings.Split(m.View(), "\n") {
		plain := ansi.Strip(line)
		if j := strings.Index(plain, text); j >= 0 {
			return ansi.StringWidth(plain[:j]), i
		}
	}
	t.Fatalf("%q is not on screen:\n%s", text, ansi.Strip(m.View()))
	return 0, 0
}

func click(m Model, x, y int) Model {
	next, _ := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	return next.(Model)
}

func wheel(m Model, x, y int, b tea.MouseButton) Model {
	next, _ := m.Update(tea.MouseMsg{X: x, Y: y, Button: b, Action: tea.MouseActionPress})
	return next.(Model)
}

func mouseBoard() Model {
	m := board(120, 30,
		item("w1", "repo", "alpha task", store.StateWorking, ""),
		item("w2", "repo", "beta task", store.StateWorking, ""),
		item("w3", "repo", "gamma task", store.StateWorking, ""),
		item("i1", "repo", "idle task", store.StateDone, ""),
	)
	t0 := time.Unix(0, 0)
	m.now = func() time.Time { return t0 }
	return m
}

func TestClickSelectsTheCardUnderThePointer(t *testing.T) {
	m := mouseBoard()
	x, y := at(t, m, "gamma task")
	m = click(m, x, y)
	if m.col != 1 || m.sel != "w3" {
		t.Fatalf("col=%d sel=%q, want Working / w3", m.col, m.sel)
	}
	if m.detail {
		t.Fatal("one click selects; it must not open the detail pane")
	}
}

func TestClickOnACardsSecondLineSelectsThatCard(t *testing.T) {
	// Cards are two lines. A one-line-per-row count would put a click on
	// alpha's second line on beta.
	m := mouseBoard()
	x, y := at(t, m, "alpha task")
	m = click(m, x, y+1)
	if m.sel != "w1" {
		t.Fatalf("sel=%q, want w1", m.sel)
	}
}

func TestSecondClickOnTheSelectedCardOpensDetail(t *testing.T) {
	m := mouseBoard()
	x, y := at(t, m, "beta task")
	m = click(m, x, y)
	m = click(m, x, y)
	if !m.detail || m.sel != "w2" {
		t.Fatalf("detail=%v sel=%q, want the detail of w2", m.detail, m.sel)
	}
}

// A key between two clicks breaks the pair. Click a card, j, k back to it,
// click again inside the double-click window: that is a fresh first click,
// so it selects and must not open the detail pane.
func TestKeyBetweenClicksBreaksTheDoubleClick(t *testing.T) {
	m := mouseBoard()
	x, y := at(t, m, "beta task")
	m = click(m, x, y)
	for _, k := range []string{"j", "k"} {
		next, _ := m.Update(keyMsg(k))
		m = next.(Model)
	}
	if m.sel != "w2" {
		t.Fatalf("sel=%q after j, k; want back on w2", m.sel)
	}
	m = click(m, x, y)
	if m.detail {
		t.Fatal("a key between the clicks must break the double click; detail opened")
	}
	if m.sel != "w2" {
		t.Fatalf("sel=%q, want w2 still selected", m.sel)
	}
}

func TestClickInAnotherColumnFocusesIt(t *testing.T) {
	m := mouseBoard()
	x, y := at(t, m, "idle task")
	m = click(m, x, y)
	if m.col != 3 || m.sel != "i1" {
		t.Fatalf("col=%d sel=%q, want Idle / i1", m.col, m.sel)
	}
}

func TestWheelMovesTheColumnUnderThePointer(t *testing.T) {
	m := mouseBoard()
	x, y := at(t, m, "alpha task")
	m = click(m, x, y)
	m = wheel(m, x, y, tea.MouseButtonWheelDown)
	// Three steps from alpha in a column of three wraps back to alpha, as
	// three j presses would.
	if m.col != 1 || m.sel != "w1" {
		t.Fatalf("col=%d sel=%q", m.col, m.sel)
	}
	m = wheel(m, x, y, tea.MouseButtonWheelUp)
	if m.sel != "w1" {
		t.Fatalf("wheel up from alpha: sel=%q", m.sel)
	}
}

func TestHelpOwnsTheMouse(t *testing.T) {
	m := press(mouseBoard(), "?")
	if !m.help {
		t.Fatal("? opens help")
	}
	before := m.sel
	r := m.helpRect()
	// A click inside the box is not a click on the board beneath it.
	m = click(m, r.X+1, r.Y+1)
	if !m.help || m.sel != before {
		t.Fatal("a click inside help must not reach the board")
	}
	m = click(m, 0, m.h-1)
	if m.help {
		t.Fatal("a click outside help closes it")
	}
}

func TestDetailIgnoresTheMouse(t *testing.T) {
	m := mouseBoard()
	x, y := at(t, m, "alpha task")
	m = click(click(m, x, y), x, y)
	if !m.detail {
		t.Fatal("setup: detail should be open")
	}
	m = click(m, 0, 2)
	if !m.detail || m.sel != "w1" {
		t.Fatal("the detail pane does not take clicks; esc closes it")
	}
}

func TestNarrowBoardClicksTheFocusedColumn(t *testing.T) {
	m := mouseBoard()
	m.w = 60 // under four columns of minColWidth: one column is drawn
	m.col = 1
	m.setRow(0)
	x, y := at(t, m, "beta task")
	m = click(m, x, y)
	if m.col != 1 || m.sel != "w2" {
		t.Fatalf("col=%d sel=%q, want Working / w2", m.col, m.sel)
	}
}

func TestMotionAndReleaseDoNothing(t *testing.T) {
	m := mouseBoard()
	x, y := at(t, m, "gamma task")
	for _, a := range []tea.MouseAction{tea.MouseActionRelease, tea.MouseActionMotion} {
		next, _ := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: a})
		if next.(Model).sel == "w3" {
			t.Fatalf("action %v selected a card", a)
		}
	}
}
