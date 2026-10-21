package tui

import (
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/theme"
	"github.com/muesli/termenv"
)

// selBoard is mouseBoard with a renderer that emits SGR, so the reverse-video
// highlight is visible in View() and its cells can be located.
func selBoard() Model {
	m := mouseBoard()
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.ANSI)
	m.th = theme.NewWith(r, theme.DefaultRoles(), theme.Glyphs{})
	return m
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

// span is one run of reverse-video cells in a rendered frame.
type span struct {
	y, x0, x1 int // x1 exclusive
	text      string
}

// highlighted finds the reverse-video runs Render drew.
func highlighted(view string) []span {
	const on, off = "\x1b[7m", "\x1b[0m"
	var out []span
	for y, line := range strings.Split(view, "\n") {
		rest, base := line, 0
		for {
			i := strings.Index(rest, on)
			if i < 0 {
				break
			}
			x0 := base + ansi.StringWidth(ansi.Strip(rest[:i]))
			body := rest[i+len(on):]
			j := strings.Index(body, off)
			if j < 0 {
				j = len(body)
			}
			text := ansi.Strip(body[:j])
			w := ansi.StringWidth(text)
			out = append(out, span{y, x0, x0 + w, text})
			base = x0 + w
			rest = body[j:]
		}
	}
	return out
}

// sepAfter is the screen column of the first separator right of x on row y,
// as View drew it.
func sepAfter(t *testing.T, m Model, x, y int) int {
	t.Helper()
	plain := []rune(ansi.Strip(strings.Split(m.View(), "\n")[y]))
	col := 0
	for _, r := range plain {
		if col > x && r == '│' {
			return col
		}
		col += ansi.StringWidth(string(r))
	}
	t.Fatalf("no separator right of %d on row %d", x, y)
	return 0
}

func TestDragStaysInsideTheColumnUnderThePress(t *testing.T) {
	m := selBoard()
	ax, ay := at(t, m, "alpha task")
	ix, iy := at(t, m, "idle task")
	sep := sepAfter(t, m, ax, ay)

	m, _ = send(m, mpress(ax, ay))
	m, _ = send(m, mmotion(ix+4, iy+1)) // deep into the Idle column
	m, cmd := send(m, mrelease(ix+4, iy+1))
	if cmd == nil {
		t.Fatal("a finished drag must return the copy command")
	}
	got := m.textSel.Text(m.selPanes(), m.View())
	if !strings.Contains(got, "alpha task") {
		t.Fatalf("copy %q lacks the dragged card", got)
	}
	if strings.Contains(got, "idle") || strings.Contains(got, "│") {
		t.Fatalf("copy %q leaked the neighbor column or the separator", got)
	}
	spans := highlighted(m.View())
	if len(spans) == 0 {
		t.Fatal("the selection is not drawn")
	}
	for _, s := range spans {
		if s.x1 > sep || strings.Contains(s.text, "│") {
			t.Fatalf("highlight %+v crosses the separator at x=%d", s, sep)
		}
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
	if m.textSel.Active() || len(highlighted(m.View())) != 0 {
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
	if m.textSel.Active() || len(highlighted(m.View())) != 0 {
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
	for range 4 {
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
	if m.textSel.Active() || len(highlighted(m.View())) != 0 {
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
