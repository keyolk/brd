package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/mouse"
	"github.com/keyolk/tuikit/toast"
)

// Mouse is an augmentation of the keyboard, not a second command set: every
// gesture below resolves to an action from the binding table and runs through
// run, so a click does exactly what the key it stands for does.
//
//   - card: focus its column and select it; a second click on the selected
//     card opens the detail pane (actDetail, the mouse form of enter).
//   - column title: focus the column.
//   - wheel over a column: focus it and move the selection three cards.
//   - help overlay: the wheel scrolls it and a click outside closes it; the
//     board under it gets nothing, as with keys.
//   - detail pane: nothing — esc closes it, as before.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.quitting || m.w == 0 {
		return m, nil
	}
	if m.help {
		return m.helpMouse(msg)
	}
	if m.detail {
		return m, nil
	}

	cols := m.columnRects()
	idx := mouse.Column(cols, msg.X, msg.Y)
	if idx < 0 {
		return m, nil
	}
	col := idx
	if len(cols) == 1 {
		col = m.col // the narrow layout draws only the focused column
	}

	if d := mouse.Delta(msg); d != 0 {
		m.focus(col)
		act := actDown
		if d < 0 {
			act, d = actUp, -d
		}
		var cmd tea.Cmd
		next := tea.Model(m)
		for range d {
			var c tea.Cmd
			next, c = next.(Model).run(act)
			cmd = tea.Batch(cmd, c)
		}
		return next, cmd
	}

	if !mouse.IsLeftPress(msg) {
		return m, nil
	}
	// A click is a keystroke for toasts: an error seen before it is read.
	m.toasts = toast.OnKey(m.toasts)
	hit := m.cardHit(col, cols[idx], msg)
	m.focus(col)
	if !hit.HasItem {
		m.clicks = mouse.Clicks{} // a title or blank click breaks a double
		return m, nil
	}
	in := m.inColumn(col)
	id := in[hit.Index].ID
	wasSelected := m.sel == id
	m.setRow(hit.Index)
	var double bool
	m.clicks, double = m.clicks.Press(id, m.clock())
	if double && wasSelected {
		return m.run(actDetail)
	}
	return m, nil
}

// helpMouse keeps the overlay's ownership of the mouse: nothing reaches the
// board while it is up.
func (m Model) helpMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if d := mouse.Delta(msg); d != 0 {
		act := actHelpDown
		if d < 0 {
			act, d = actHelpUp, -d
		}
		next := tea.Model(m)
		for range d {
			next, _ = next.(Model).run(act)
		}
		return next, nil
	}
	if mouse.IsLeftPress(msg) && !m.helpRect().Contains(msg.X, msg.Y) {
		return m.run(actHelpClose)
	}
	return m, nil
}

// focus moves the cursor to column col, keeping the row position as the
// column keys do.
func (m *Model) focus(col int) {
	if col == m.col {
		m.resolve()
		return
	}
	m.col = col
	m.setRow(m.row)
}

// columnRects is where View draws the columns: below the one-line header,
// bodyHeight tall, four columns with a one-cell separator — or one
// full-width column when the terminal is too narrow for four. It reads the
// same model state renderColumns does, so hit tests and drawing agree.
func (m Model) columnRects() []mouse.Rect {
	body := m.bodyRect()
	if (m.w-(len(Columns)-1))/len(Columns) < minColWidth {
		return []mouse.Rect{body}
	}
	return mouse.SplitColumns(body, len(Columns), 1)
}

// bodyRect is the area between the header and the bottom bar, where View
// draws the columns or the detail pane.
func (m Model) bodyRect() mouse.Rect {
	return mouse.Rect{X: 0, Y: 1, W: m.w, H: m.bodyHeight()}
}

// cardHit maps a click inside column col (drawn in r) to a card. The card
// area starts below the title, the empty-column hint and the "↑ N more"
// line, exactly as renderColumn and renderSingle lay them out.
func (m Model) cardHit(col int, r mouse.Rect, msg tea.MouseMsg) mouse.Click {
	avail := r.H - 1 // title line
	start, _ := m.columnWindow(col, avail)
	top := r.Y + 1
	if len(m.inColumn(col)) == 0 {
		return mouse.Click{Index: -1, Visible: true}
	}
	if start > 0 {
		top++ // "↑ N more"
	}
	area := mouse.Rect{X: r.X, Y: top, W: r.W, H: r.Y + r.H - top}
	return mouse.BlockHit(area, start, blockHeights(m.cardBlocks(col, minColWidth, false)), msg)
}

// helpRect is where View centers the help box.
func (m Model) helpRect() mouse.Rect {
	lines, _, w, h := m.helpLines(m.helpOff)
	if lines == nil {
		return mouse.Rect{}
	}
	return mouse.Rect{X: max(0, (m.w-w)/2), Y: max(0, (m.h-h)/2), W: w, H: h}
}

func (m Model) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}
