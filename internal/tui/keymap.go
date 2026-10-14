package tui

import (
	"github.com/keyolk/tuikit/keys"
)

// keymap.go is the one binding table. Dispatch, the hint bar and the `?` help
// all read it, so a key cannot do one thing while the help says another.
//
// Korean-layout normalization (ㅂ → q) happens inside keys.Resolve: this TUI
// has no text entry, so every key is a shortcut.

const (
	ctxGlobal keys.Ctx = "global"
	ctxBoard  keys.Ctx = "board"
	ctxDetail keys.Ctx = "detail"
	ctxHelp   keys.Ctx = "help"
)

// Actions, named so a user override file can address them.
const (
	actQuit        = "quit"
	actHelp        = "help"
	actRefresh     = "refresh"
	actDown        = "move.down"
	actUp          = "move.up"
	actPrevCol     = "column.prev"
	actNextCol     = "column.next"
	actDetail      = "detail.toggle"
	actBlocked     = "blocked"
	actGroup       = "group"
	actJump        = "jump"
	actResume      = "resume"
	actDetailClose = "detail.close"
	actHelpClose   = "help.close"
	actHelpDown    = "help.down"
	actHelpUp      = "help.up"
)

// defaultKeymap is the table before user overrides. Footer numbers order the
// hint bar: lower survives longer when the terminal is narrow.
func defaultKeymap() keys.Keymap {
	return keys.New(
		keys.Binding{Action: actQuit, Keys: []string{"q", "ctrl+c"}, Ctx: ctxGlobal, Group: "App", Help: "quit", Pinned: true, Footer: 2},
		keys.Binding{Action: actHelp, Keys: []string{"?"}, Ctx: ctxGlobal, Group: "App", Help: "help", Pinned: true, Footer: 1},
		keys.Binding{Action: actRefresh, Keys: []string{"r"}, Ctx: ctxGlobal, Group: "App", Help: "refresh", Footer: 8},

		keys.Binding{Action: actDetail, Keys: []string{"enter", " "}, Ctx: ctxBoard, Group: "Act", Help: "detail", Footer: 1},
		// J is unreachable under a Korean input source: shift+ㅓ is still ㅓ,
		// which normalizes to j and moves the cursor instead. g is the
		// reachable alias; J stays first so the hint keeps its old look.
		keys.Binding{Action: actJump, Keys: []string{"J", "g"}, Ctx: ctxBoard, Group: "Act", Help: "jump", Footer: 2},
		keys.Binding{Action: actResume, Keys: []string{"R"}, Ctx: ctxBoard, Group: "Act", Help: "resume", Footer: 3},
		keys.Binding{Action: actBlocked, Keys: []string{"b"}, Ctx: ctxBoard, Group: "Move", Help: "blocked", Footer: 4},
		// One action whose label follows the board: the hint says which way
		// t goes, so it reads "ungroup" once the board is grouped.
		keys.Binding{Action: actGroup, Keys: []string{"t"}, Ctx: ctxBoard, Group: "View", Help: "group",
			Labels: map[string]string{stateOn: "ungroup"}, Footer: 5},
		// Declared h, j, k, l so the collapsed footer hint reads `hjkl move`.
		keys.Binding{Action: actPrevCol, Keys: []string{"h", "left"}, Ctx: ctxBoard, Group: "Move", Help: "prev column", Footer: 6, FooterGroup: "move"},
		keys.Binding{Action: actDown, Keys: []string{"j", "down"}, Ctx: ctxBoard, Group: "Move", Help: "down", Footer: 6, FooterGroup: "move"},
		keys.Binding{Action: actUp, Keys: []string{"k", "up"}, Ctx: ctxBoard, Group: "Move", Help: "up", Footer: 6, FooterGroup: "move"},
		keys.Binding{Action: actNextCol, Keys: []string{"l", "right"}, Ctx: ctxBoard, Group: "Move", Help: "next column", Footer: 6, FooterGroup: "move"},

		// q closes the pane before it quits the app: the layering predates
		// the table, and esc alone is not what people reach for first. The
		// footer then offers ctrl+c for quit, so nothing it shows is a lie.
		keys.Binding{Action: actDetailClose, Keys: []string{"esc", "q"}, Ctx: ctxDetail, Group: "Detail", Help: "close", Footer: 1,
			Shadows: []string{actQuit}},

		keys.Binding{Action: actHelpClose, Keys: []string{"esc"}, Ctx: ctxHelp, Group: "Help", Help: "close help", Footer: 1},
		keys.Binding{Action: actHelpDown, Keys: []string{"j", "down"}, Ctx: ctxHelp, Group: "Help", Help: "scroll down"},
		keys.Binding{Action: actHelpUp, Keys: []string{"k", "up"}, Ctx: ctxHelp, Group: "Help", Help: "scroll up"},
	)
}

// stateOn is the Labels state of a toggle that is currently on.
const stateOn = "on"

// stacks are every context stack the board can show, innermost first. The
// conflict check and the override loader both read them, so a user rebinding
// cannot create a shadow the tests would have caught.
var stacks = [][]keys.Ctx{
	{ctxBoard, ctxGlobal},
	{ctxDetail, ctxBoard, ctxGlobal},
	{ctxHelp, ctxGlobal},
}

// baseKeymap backs models built without New (tests construct Model literals).
var baseKeymap = defaultKeymap()

// keymap is the table in effect: the user's, or the defaults for a zero km.
func (m Model) keymap() keys.Keymap { return m.km.Or(baseKeymap) }

// active is the context stack, innermost first.
func (m Model) active() []keys.Ctx {
	switch {
	case m.help:
		return stacks[2]
	case m.detail:
		return stacks[1]
	}
	return stacks[0]
}

// keyOpts is the model's view of its actions for one Resolve, Footer or Help
// call. Nothing is disabled: every key keeps working even when the hint bar
// does not offer it, as it did before the table.
func (m Model) keyOpts() keys.Opts {
	return keys.Opts{Visible: m.inFooter, State: m.keyState}
}

func (m Model) keyState(action string) string {
	if action == actGroup && m.group {
		return stateOn
	}
	return ""
}

// inFooter decides which keys the hint bar offers: only the ones that would
// do something visible right now. A bar that lists everything pushes the keys
// a lost user needs off its end.
func (m Model) inFooter(action string) bool {
	switch action {
	case actDown, actUp, actPrevCol, actNextCol:
		return !m.detail && len(m.items) > 0
	case actDetail:
		return !m.detail && len(m.items) > 0
	case actJump, actResume:
		_, ok := m.selected()
		return ok
	case actBlocked:
		return !m.detail && m.col != 0 && len(m.inColumn(0)) > 0
	case actGroup:
		// Ungrouping is always worth offering; grouping only once two items
		// share a group, below which it is a key that visibly does nothing.
		return m.group || m.sharedGroups()
	}
	return true
}

// helpSections documents the board and the detail pane together, so the help
// page lists the pane's keys while the board is showing.
func (m Model) helpSections() []keys.Section {
	return m.keymap().Help(m.keyOpts(), stacks[0], stacks[1])
}
