package tui

import (
	"os"
	"path/filepath"
	"slices"

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
	actGroup       = "group.on"
	actUngroup     = "group.off"
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
		// group.on and group.off share t and are mutually exclusive through
		// enabled(): the kit has no state-dependent label, and the hint must
		// say which way t goes.
		keys.Binding{Action: actGroup, Keys: []string{"t"}, Ctx: ctxBoard, Group: "View", Help: "group", Footer: 5},
		keys.Binding{Action: actUngroup, Keys: []string{"t"}, Ctx: ctxBoard, Group: "View", Help: "ungroup", Footer: 5},
		keys.Binding{Action: actDown, Keys: []string{"j", "down"}, Ctx: ctxBoard, Group: "Move", Help: "down", Footer: 6},
		keys.Binding{Action: actUp, Keys: []string{"k", "up"}, Ctx: ctxBoard, Group: "Move", Help: "up", Footer: 6},
		keys.Binding{Action: actPrevCol, Keys: []string{"h", "left"}, Ctx: ctxBoard, Group: "Move", Help: "prev column", Footer: 7},
		keys.Binding{Action: actNextCol, Keys: []string{"l", "right"}, Ctx: ctxBoard, Group: "Move", Help: "next column", Footer: 7},

		// q closes the pane before it quits the app: the layering predates
		// the table, and esc alone is not what people reach for first.
		keys.Binding{Action: actDetailClose, Keys: []string{"esc", "q"}, Ctx: ctxDetail, Group: "Detail", Help: "close", Footer: 1},

		keys.Binding{Action: actHelpClose, Keys: []string{"esc"}, Ctx: ctxHelp, Group: "Help", Help: "close help", Footer: 1},
		keys.Binding{Action: actHelpDown, Keys: []string{"j", "down"}, Ctx: ctxHelp, Group: "Help", Help: "scroll down"},
		keys.Binding{Action: actHelpUp, Keys: []string{"k", "up"}, Ctx: ctxHelp, Group: "Help", Help: "scroll up"},
	)
}

// baseKeymap backs models built without New (tests construct Model literals):
// a zero keys.Keymap resolves nothing.
var baseKeymap = defaultKeymap()

// KeysPath is the user override file, `{action: [keys...]}` in YAML.
func KeysPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "brd", "keys.yaml")
}

// keymap is the table in effect.
func (m Model) keymap() keys.Keymap {
	if m.km != nil {
		return *m.km
	}
	return baseKeymap
}

// active is the context stack, innermost first.
func (m Model) active() []keys.Ctx {
	switch {
	case m.help:
		return []keys.Ctx{ctxHelp, ctxGlobal}
	case m.detail:
		return []keys.Ctx{ctxDetail, ctxBoard, ctxGlobal}
	}
	return []keys.Ctx{ctxBoard, ctxGlobal}
}

// canRun gates dispatch. Only the group pair is gated: every other key keeps
// working even when the hint bar does not offer it, as it did before the table.
func (m Model) canRun(action string) bool {
	switch action {
	case actGroup:
		return !m.group
	case actUngroup:
		return m.group
	}
	return true
}

// inFooter decides which runnable keys the hint bar offers: only the ones that
// would do something visible right now. A bar that lists everything pushes
// the keys a lost user needs off its end.
func (m Model) inFooter(action string) bool {
	if !m.canRun(action) {
		return false
	}
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
		// Grouping is only worth offering once two items share a group;
		// below that it is a key that visibly does nothing.
		return m.sharedGroups()
	}
	return true
}

// helpSections lists every context's keys, not just the active stack's, so
// the help page documents the detail pane while the board is showing.
func (m Model) helpSections() []keys.Section {
	km := m.keymap()
	sections := append(km.Help([]keys.Ctx{ctxBoard, ctxGlobal}), km.Help([]keys.Ctx{ctxDetail})...)
	// The space bar's key string is " ", which reads as nothing in a list of
	// keys; the kit has no display names, so name it here.
	for _, sec := range sections {
		for i := range sec.Entries {
			ks := slices.Clone(sec.Entries[i].Keys)
			for j, k := range ks {
				if k == " " {
					ks[j] = "space"
				}
			}
			sec.Entries[i].Keys = ks
		}
	}
	return sections
}
