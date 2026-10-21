package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/selection"
	"github.com/keyolk/tuikit/tuitest"
)

// selBoard is mouseBoard; the highlight is selection.DefaultStyle, which
// emits reverse video under any color profile, so tests see it without a
// theme renderer of their own.
func selBoard() Model { return mouseBoard() }

// captureCopies replaces the clipboard with a recorder, so a test runs the
// copy command the app returned and asserts what reached the clipboard.
func captureCopies(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old := selection.Clipboard
	selection.Clipboard = func(s string) error {
		got = append(got, s)
		return nil
	}
	t.Cleanup(func() { selection.Clipboard = old })
	return &got
}

func send(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func mpress(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
}

func mmotion(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion}
}

func mrelease(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonNone, Action: tea.MouseActionRelease}
}

func TestDragStaysInsideTheColumnUnderThePress(t *testing.T) {
	copies := captureCopies(t)
	m := selBoard()
	ax, ay := at(t, m, "alpha task")
	ix, iy := at(t, m, "idle task")

	m, _ = send(m, mpress(ax, ay))
	m, _ = send(m, mmotion(ix+4, iy+1)) // deep into the Idle column
	m, cmd := send(m, mrelease(ix+4, iy+1))
	if cmd == nil {
		t.Fatal("a finished drag must return the copy command")
	}
	for _, msg := range tuitest.Drain(cmd, 0) {
		m, _ = send(m, msg)
	}
	if len(*copies) != 1 {
		t.Fatalf("the release copied %d times, want once", len(*copies))
	}
	got := (*copies)[0]
	if !strings.Contains(got, "alpha task") {
		t.Fatalf("copy %q lacks the dragged card", got)
	}
	if strings.Contains(got, "idle") || strings.Contains(got, "│") {
		t.Fatalf("copy %q leaked the neighbor column or the separator", got)
	}
	runs := tuitest.Reversed(m.View())
	if len(runs) == 0 {
		t.Fatal("the selection is not drawn")
	}
	for _, r := range runs {
		if strings.Contains(r, "│") || strings.Contains(r, "idle") {
			t.Fatalf("highlight %q crosses the separator into the next column", r)
		}
	}
	if !strings.Contains(ansi.Strip(m.View()), "copied ") {
		t.Fatal("the copy should show its toast")
	}
}

func TestPressWithoutMotionIsStillAClick(t *testing.T) {
	m := selBoard()
	x, y := at(t, m, "gamma task")
	m, _ = send(m, mpress(x, y))
	m, _ = send(m, mrelease(x, y))
	if m.sel != "w3" {
		t.Fatalf("sel=%q, want w3: a still press is the app's click", m.sel)
	}
	if m.textSel.Active() || len(tuitest.Reversed(m.View())) != 0 {
		t.Fatal("a click without motion must not leave a selection")
	}
}

func TestSecondClickOnACardIsStillEnterNotAWordPick(t *testing.T) {
	m := selBoard()
	x, y := at(t, m, "beta task")
	m, _ = send(m, mpress(x, y))
	m, _ = send(m, mrelease(x, y))
	m, _ = send(m, mpress(x, y))
	if !m.detail || m.sel != "w2" {
		t.Fatalf("detail=%v sel=%q: a list keeps second click = Enter", m.detail, m.sel)
	}
	if m.textSel.Active() {
		t.Fatal("a list pane must not pick a word on a double click")
	}
}

func TestKeyClearsTheSelection(t *testing.T) {
	m := selBoard()
	x, y := at(t, m, "alpha task")
	m, _ = send(m, mpress(x, y))
	m, _ = send(m, mmotion(x+4, y))
	m, _ = send(m, mrelease(x+4, y))
	if !m.textSel.Active() {
		t.Fatal("setup: the drag should leave a selection")
	}
	m, _ = send(m, keyMsg("j"))
	if m.textSel.Active() || len(tuitest.Reversed(m.View())) != 0 {
		t.Fatal("a key must clear the selection")
	}
}

func TestDragPastTheHelpBottomScrollsAndKeepsScrolledOutLines(t *testing.T) {
	m := selBoard()
	m.h = 14 // help is taller than the screen, so it scrolls
	m = press(m, "?")
	x, y := at(t, m, "Act")
	_, bottom := at(t, m, "╰")
	if strings.Contains(ansi.Strip(m.View()), "next column") {
		t.Fatal("setup: next column should be below the help page")
	}
	m, _ = send(m, mpress(x, y))
	// One motion per line: a drag past the edge scrolls one line per event.
	for range 5 {
		m, _ = send(m, mmotion(x+40, bottom))
	}
	if m.helpOff == 0 {
		t.Fatal("a drag past the bottom must scroll the help")
	}
	if !strings.Contains(ansi.Strip(m.View()), "next column") {
		t.Fatal("the scrolled page should now show next column")
	}
	m, cmd := send(m, mrelease(x+40, bottom))
	if cmd == nil {
		t.Fatal("release must return the copy command")
	}
	got := m.textSel.Text(m.selPanes(), m.View())
	if !strings.HasPrefix(got, "Act") || !strings.Contains(got, "next column") {
		t.Fatalf("copy %q must run from the scrolled-out Act to next column", got)
	}
	if strings.Contains(got, "│") || strings.Contains(got, "╰") {
		t.Fatalf("copy %q includes the box border", got)
	}
}

func TestBoardUnderHelpIsNotSelectable(t *testing.T) {
	m := selBoard()
	x, y := at(t, m, "idle task") // outside the help box
	m = press(m, "?")
	m, _ = send(m, mpress(x, y))
	m, _ = send(m, mmotion(x+5, y+1))
	m, _ = send(m, mrelease(x+5, y+1))
	if m.textSel.Active() || len(tuitest.Reversed(m.View())) != 0 {
		t.Fatal("the board under the help overlay must not be selectable")
	}
}

func TestDoubleClickInDetailPicksAWord(t *testing.T) {
	m := selBoard()
	x, y := at(t, m, "alpha task")
	m = click(click(m, x, y), x, y)
	if !m.detail {
		t.Fatal("setup: detail should be open")
	}
	sx, sy := at(t, m, "w1") // the session row's value
	m.now = func() time.Time { return time.Unix(100, 0) }
	m, _ = send(m, mpress(sx, sy))
	m, _ = send(m, mrelease(sx, sy))
	m, cmd := send(m, mpress(sx, sy))
	if cmd == nil {
		t.Fatal("a word pick copies")
	}
	if got := m.textSel.Text(m.selPanes(), m.View()); got != "w1" {
		t.Fatalf("word pick = %q, want w1", got)
	}
	if !m.detail {
		t.Fatal("a word pick must not close the detail pane")
	}
}

// The "… N more" row of the help is not a help line: a drag onto it scrolls
// rather than selecting the hidden line that would sit there.
func TestDragOntoTheHelpMoreMarkerScrolls(t *testing.T) {
	m := selBoard()
	m.h = 14
	m = press(m, "?")
	x, y := at(t, m, "Act")
	_, marker := at(t, m, "more")
	m, _ = send(m, mpress(x, y))
	m, _ = send(m, mmotion(x+40, marker))
	if m.helpOff != 1 {
		t.Fatalf("helpOff=%d after a drag onto the marker, want 1", m.helpOff)
	}
}
