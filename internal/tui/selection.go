package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/keys"
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

// selectionMouse feeds a mouse event or an auto-scroll tick to the selection
// before the click handler. A press that does not move is left to
// handleMouse, so click-to-select still works; only what the selection
// consumed (a drag, its release, a word pick) skips it.
func (m Model) selectionMouse(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.quitting || m.w == 0 {
		return m, nil
	}
	r, cmd := m.textSel.Update(msg, m.selPanes, m.View, m.clock())
	if r.Scroll != 0 && r.ScrollPane == helpPaneID {
		// Set through the clamp the help keys use: past the last page the
		// kit's offset can be one more than RenderHelp draws, and a stored
		// unclamped value would make the next drag up a no-op.
		_, m.helpOff, _, _ = m.helpLines(r.Offset)
	}
	mm, isMouse := msg.(tea.MouseMsg)
	if r.Consumed || !isMouse {
		return m, cmd
	}
	next, click := m.handleMouse(mm)
	return next, tea.Batch(cmd, click)
}

// copied turns the clipboard write's outcome into a toast: a copy is silent
// otherwise, and a failed one (no pbcopy, no /dev/tty) must not look like a
// success.
func (m Model) copied(msg selection.CopiedMsg) (tea.Model, tea.Cmd) {
	lvl := toast.Info
	if msg.Err != nil {
		lvl = toast.Error
	}
	return m, m.notify("copy", lvl, msg.Note())
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
	content := box.InsetStyle(helpBox)
	all := keys.HelpLines(m.helpSections(), content.W, m.th.HelpStyle())
	// The offset View draws at: helpOff clamped to the last page. A stored
	// value past it (restored state, a resize that made the page taller)
	// would make the pane's lines disagree with the screen.
	_, off, _, _ := m.helpLines(m.helpOff)
	if len(all)-off > content.H {
		// RenderHelp spends the last row on "… N more" while lines remain
		// below. That row is not Lines[Offset+ch-1], so it is left out of the
		// pane: a drag onto it scrolls instead of selecting a hidden line.
		content.H--
	}
	return selection.Pane{ID: helpPaneID, Rect: content, Kind: selection.Text, Lines: all, Offset: off}, true
}
