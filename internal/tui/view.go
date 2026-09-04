package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/brd/internal/store"
)

// Palette. Only the Blocked column gets a warm color: if every column were
// colored, none of them would carry a signal, and the board's whole job is
// making one column jump out.
var (
	blockedFg = lipgloss.AdaptiveColor{Light: "#b3261e", Dark: "#f2b8b5"}
	workingFg = lipgloss.AdaptiveColor{Light: "#1b5e20", Dark: "#a5d6a7"}
	dimFg     = lipgloss.AdaptiveColor{Light: "#6b6b6b", Dark: "#8a8a8a"}
	accentFg  = lipgloss.AdaptiveColor{Light: "#0b57d0", Dark: "#a8c7fa"}

	dim       = lipgloss.NewStyle().Foreground(dimFg)
	bold      = lipgloss.NewStyle().Bold(true)
	headStyle = lipgloss.NewStyle().Bold(true).Foreground(dimFg)
	selStyle  = lipgloss.NewStyle().Bold(true).Foreground(accentFg)
)

// minColWidth is the narrowest a column can be and still show a repo name
// beside an age. Below this the board stacks into a single list instead of
// rendering four unreadable slivers.
const minColWidth = 22

func (m Model) View() string {
	if m.err != nil {
		return "\n  " + lipgloss.NewStyle().Foreground(blockedFg).Render(
			"board unavailable: "+m.err.Error()) + "\n\n  r retry · q quit\n"
	}
	if m.w == 0 {
		return "" // pre-first-WindowSizeMsg; drawing now would flash a wrong layout
	}

	header := m.renderHeader()
	hints := m.renderHints()
	// Reserve exactly what the chrome uses so the columns never overflow into
	// the hint bar and push it off screen.
	bodyH := m.h - lipgloss.Height(header) - lipgloss.Height(hints)
	if bodyH < 3 {
		bodyH = 3
	}

	var body string
	if m.detail {
		body = m.renderDetail(bodyH)
	} else {
		body = m.renderColumns(bodyH)
	}
	return header + "\n" + body + "\n" + hints
}

// renderHeader leads with the blocked count, because that is the number the
// user opened the board to read.
func (m Model) renderHeader() string {
	blocked := len(m.inColumn(0))
	live := len(m.inColumn(1)) + len(m.inColumn(2))

	left := bold.Render("brd")
	var parts []string
	if blocked > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(blockedFg).Bold(true).
			Render(fmt.Sprintf("%d waiting on you", blocked)))
	}
	if live > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(workingFg).
			Render(fmt.Sprintf("%d live", live)))
	}
	if len(parts) == 0 {
		parts = append(parts, dim.Render("idle"))
	}
	return left + "  " + strings.Join(parts, dim.Render(" · "))
}

// renderHints shows only the keys that do something right now.
//
// A hint bar wider than the terminal truncates from the right, and what
// falls off is whatever was listed last — so a bar that lists everything
// hides the keys a lost user needs most. Keys appear here only when they
// would act.
func (m Model) renderHints() string {
	var keys []string
	if m.detail {
		keys = append(keys, "esc close")
	} else {
		if len(m.items) > 0 {
			keys = append(keys, "hjkl move", "↵ detail")
		}
		if len(m.inColumn(0)) > 0 && m.col != 0 {
			keys = append(keys, "b blocked")
		}
	}
	keys = append(keys, "r refresh", "q quit")
	return dim.Render(strings.Join(keys, " · "))
}

func (m Model) renderColumns(height int) string {
	// Narrow terminals get the focused column full-width rather than four
	// columns too thin to read. Squeezing all four is what makes a board
	// useless on a split pane.
	perCol := (m.w - (len(Columns) - 1)) / len(Columns)
	if perCol < minColWidth {
		return m.renderSingle(height)
	}

	rendered := make([]string, 0, len(Columns))
	for i, col := range Columns {
		rendered = append(rendered, m.renderColumn(i, col, perCol, height))
	}
	sep := dim.Render(strings.TrimRight(strings.Repeat("│\n", height), "\n"))
	joined := make([]string, 0, len(rendered)*2-1)
	for i, r := range rendered {
		if i > 0 {
			joined = append(joined, sep)
		}
		joined = append(joined, r)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, joined...)
}

func (m Model) renderColumn(idx int, col Column, w, height int) string {
	in := m.inColumn(idx)
	focused := idx == m.col

	title := fmt.Sprintf("%s %d", col.Title, len(in))
	if focused {
		title = selStyle.Render(title)
	} else if idx == 0 && len(in) > 0 {
		title = lipgloss.NewStyle().Foreground(blockedFg).Bold(true).Render(title)
	} else {
		title = headStyle.Render(title)
	}
	lines := []string{title}

	if len(in) == 0 {
		lines = append(lines, dim.Render(truncate(col.Hint, w)))
	}
	for i, it := range in {
		lines = append(lines, m.renderCard(it, i == m.row && focused, w)...)
		if len(lines) >= height {
			break
		}
	}
	// Pad so JoinHorizontal aligns every column's separator to full height.
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lipgloss.NewStyle().Width(w).MaxHeight(height).
		Render(strings.Join(lines[:height], "\n"))
}

// renderCard is two lines: what the work is, and why it is in this column.
//
// The second line is the point. "Blocked, 12m" says nothing actionable;
// "permission_prompt 12m" says which keystroke ends it.
func (m Model) renderCard(it store.Item, selected bool, w int) []string {
	marker := "  "
	titleStyle := lipgloss.NewStyle()
	if selected {
		marker = selStyle.Render("▌ ")
		titleStyle = titleStyle.Bold(true)
	}
	title := titleStyle.Render(truncate(Label(it), w-2))

	meta := ShortAge(it.Age())
	if it.BlockedOn != "" {
		meta += " " + it.BlockedOn
	} else if n := len(it.Children); n > 0 {
		meta += " " + childSummary(it.Children)
	}
	metaStyle := dim
	if it.BlockedOn != "" {
		metaStyle = lipgloss.NewStyle().Foreground(blockedFg)
	}
	sub := dim.Render(RepoName(it.Repo)) + " " + metaStyle.Render(meta)

	return []string{marker + title, "  " + truncate(sub, w-2)}
}

// childSummary collapses children to counts per kind, so an item running six
// shell jobs takes one line rather than six.
func childSummary(kids []store.Child) string {
	counts := map[string]int{}
	order := []string{}
	for _, c := range kids {
		if counts[c.Kind] == 0 {
			order = append(order, c.Kind)
		}
		counts[c.Kind]++
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		if counts[k] == 1 {
			parts = append(parts, k)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s×%d", k, counts[k]))
	}
	return strings.Join(parts, " ")
}

// renderSingle is the narrow-terminal layout: the focused column only.
func (m Model) renderSingle(height int) string {
	col := Columns[m.col]
	in := m.inColumn(m.col)
	lines := []string{selStyle.Render(fmt.Sprintf("%s %d", col.Title, len(in))) +
		dim.Render(fmt.Sprintf("  (%d/%d)", m.col+1, len(Columns)))}
	if len(in) == 0 {
		lines = append(lines, dim.Render(col.Hint))
	}
	for i, it := range in {
		lines = append(lines, m.renderCard(it, i == m.row, m.w)...)
		if len(lines) >= height {
			break
		}
	}
	return lipgloss.NewStyle().MaxHeight(height).Render(strings.Join(lines, "\n"))
}

// renderDetail shows everything known about one item, including the children
// the card had to summarize.
func (m Model) renderDetail(height int) string {
	it, ok := m.selected()
	if !ok {
		return dim.Render("nothing selected")
	}
	var b strings.Builder
	b.WriteString(bold.Render(truncate(Label(it), m.w)) + "\n")

	row := func(k, v string) {
		if v == "" {
			return
		}
		b.WriteString(dim.Render(fmt.Sprintf("%-10s ", k)) +
			truncate(v, m.w-11) + "\n")
	}
	state := it.State
	if it.BlockedOn != "" {
		state += " on " + it.BlockedOn
	}
	row("state", fmt.Sprintf("%s  (%s)", state, ShortAge(it.Age())))
	row("repo", it.Repo)
	if it.Cwd != it.Repo {
		row("cwd", it.Cwd)
	}
	row("subject", it.Subject)
	row("model", it.Model)
	row("turns", fmt.Sprintf("%d", it.Turns))
	row("session", it.ID)

	if len(it.Children) > 0 {
		b.WriteString("\n" + headStyle.Render(
			fmt.Sprintf("in flight (%d)", len(it.Children))) + "\n")
		for _, c := range it.Children {
			line := fmt.Sprintf("%-12s %-22s %s", c.Kind, truncate(c.Label, 22), c.State)
			if c.Detail != "" {
				line += dim.Render("  " + c.Detail)
			}
			b.WriteString("  " + truncate(line, m.w-2) + "\n")
		}
	}
	if it.LastMessage != "" {
		b.WriteString("\n" + headStyle.Render("last message") + "\n")
		b.WriteString("  " + truncate(it.LastMessage, m.w-2) + "\n")
	}
	return lipgloss.NewStyle().MaxHeight(height).Render(b.String())
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// Label is how an item is named on the board: its title, falling back to the
// current subject and finally the session id. Exported so `brd ls` and the
// TUI can never disagree about what an item is called.
func Label(it store.Item) string {
	return firstNonEmpty(it.Title, it.Subject, it.ID)
}
