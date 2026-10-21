package tui

import (
	"fmt"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/mouse"
	"github.com/keyolk/tuikit/render"
	"github.com/keyolk/tuikit/selection"
	"github.com/keyolk/tuikit/toast"
)

// Drag selection (E14): a drag selects text inside the pane under the press,
// never across a column separator, and the release copies it. Mouse capture
// turned the terminal's own drag-select off, and Shift-drag crosses every
// column at once, which is useless on a four-column board.
//
//   - board columns are List panes: a second click on the selected card stays
//     "open detail", so they get no word or line pick.
//   - the detail pane is a Text pane read from the frame; it does not scroll.
//   - the help overlay is a Text pane with its full content, so a drag past
//     its edge scrolls it the way the wheel does. While it is open it is the
//     only pane: the dimmed board under it is not selectable.

// selectionMouse feeds msg to the selection before the click handler. A press
// that does not move is left to handleMouse, so click-to-select still works;
// only what the selection consumed (a drag, its release, a word pick) skips it.
func (m Model) selectionMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.quitting || m.w == 0 {
		return m, nil
	}
	r := m.textSel.Mouse(msg, m.selPanes(), m.View(), m.clock())
	var cmds []tea.Cmd
	next := tea.Model(m)
	if r.Scroll != 0 {
		next = m.scrollPane(r.ScrollPane, r.Scroll)
	}
	if r.Copy != "" {
		cmds = append(cmds, selection.Copy(r.Copy))
	}
	if r.Consumed {
		return next, tea.Batch(cmds...)
	}
	next, cmd := next.(Model).handleMouse(msg)
	return next, tea.Batch(append(cmds, cmd)...)
}

// scrollPane scrolls a pane by n lines through the same actions the wheel
// runs, so a drag cannot scroll further than a key could.
func (m Model) scrollPane(id string, n int) tea.Model {
	if id != helpPaneID {
		return m
	}
	act := actHelpDown
	if n < 0 {
		act, n = actHelpUp, -n
	}
	next := tea.Model(m)
	for range n {
		next, _ = next.(Model).run(act)
	}
	return next
}

// copied turns the clipboard write's outcome into a toast: a copy is silent
// otherwise, and a failed one (no pbcopy, no /dev/tty) must not look like a
// success.
func (m Model) copied(msg selection.CopiedMsg) (tea.Model, tea.Cmd) {
	if msg.Err != nil {
		return m, m.notify("copy", toast.Error, "copy failed: "+msg.Err.Error())
	}
	return m, m.notify("copy", toast.Info, fmt.Sprintf("copied %d chars", utf8.RuneCountInString(msg.Text)))
}

const helpPaneID = "help"

// selPanes lists the selectable regions from the numbers View draws with
// (bodyRect, columnRects, helpRect), in drawing order.
func (m Model) selPanes() []selection.Pane {
	if m.w == 0 {
		return nil
	}
	if m.help {
		if p, ok := m.helpPane(); ok {
			return []selection.Pane{p}
		}
		return nil
	}
	if m.detail {
		return []selection.Pane{{ID: "detail", Rect: m.bodyRect(), Kind: selection.Text}}
	}
	rects := m.columnRects()
	panes := make([]selection.Pane, 0, len(rects))
	for i, r := range rects {
		panes = append(panes, selection.Pane{ID: fmt.Sprintf("column-%d", i), Rect: r, Kind: selection.List})
	}
	return panes
}

// helpPane is the help box's content area (inside its border and padding)
// with every help line, so a drag can scroll the overlay and keep the lines
// that scrolled out. The page View draws is RenderHelp(...)[helpOff:], so the
// full list at offset 0 lines up with it row for row.
func (m Model) helpPane() (selection.Pane, bool) {
	box := m.helpRect()
	if box.W == 0 {
		return selection.Pane{}, false
	}
	cw, ch := render.BoxContent(box.W, box.H, helpBox)
	sections := m.helpSections()
	all, _ := keys.RenderHelp(sections, cw, keys.HelpLineCount(sections, cw), 0, m.th.HelpStyle())
	content := mouse.Rect{
		X: box.X + helpBox.GetMarginLeft() + helpBox.GetBorderLeftSize() + helpBox.GetPaddingLeft(),
		Y: box.Y + helpBox.GetMarginTop() + helpBox.GetBorderTopSize() + helpBox.GetPaddingTop(),
		W: cw, H: ch,
	}
	if len(all)-m.helpOff > ch {
		// RenderHelp spends the last row on "… N more" while lines remain
		// below. That row is not Lines[Offset+ch-1], so it is left out of the
		// pane: a drag onto it scrolls instead of selecting a hidden line.
		content.H--
	}
	return selection.Pane{ID: helpPaneID, Rect: content, Kind: selection.Text, Lines: all, Offset: m.helpOff}, true
}

// selectionStyle is reverse video: it reads on any palette and survives
// NO_COLOR, where the theme's renderer keeps attributes but drops colors.
func (m Model) selectionStyle() lipgloss.Style {
	if r := m.th.Renderer(); r != nil {
		return r.NewStyle().Reverse(true)
	}
	return lipgloss.NewStyle().Reverse(true)
}
