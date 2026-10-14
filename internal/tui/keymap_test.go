package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/prefs"
	"github.com/keyolk/tuikit/toast"

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
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case " ", "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// press runs one key through the real handler and returns the new model, so
// these tests cover the normalization exactly where it sits.
func press(m Model, key string) Model {
	next, _ := m.handleKey(keyMsg(key))
	return next.(Model)
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

// esc backs out of the detail pane and otherwise does nothing; only q quits.
func TestEscDoesNotQuitTheBoard(t *testing.T) {
	m := Model{}
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("esc on the bare board quit the app")
		}
	}

	m = Model{detail: true}
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if next.(Model).detail {
		t.Fatal("esc did not close the detail pane")
	}

	// Model{} has the zero Saver, which writes nothing and reports the
	// final save at once, so this needs no state directory.
	_, cmd = Model{}.handleKey(keyMsg("q"))
	if cmd == nil {
		t.Fatal("q returned no command")
	}
	if _, quit := quitAfterSave(t, Model{}, keyMsg("q")); !quit {
		t.Fatal("q on the bare board did not quit")
	}
}

// quitAfterSave presses key, runs the state write it returns, feeds the
// result back, and reports whether the board then quit. q writes the UI
// state first so a pending debounce does not die with the program.
func quitAfterSave(t *testing.T, m Model, key tea.KeyMsg) (Model, bool) {
	t.Helper()
	next, cmd := m.handleKey(key)
	if cmd == nil {
		return next.(Model), false
	}
	saved, ok := cmd().(prefs.Saved)
	if !ok {
		return next.(Model), false
	}
	if saved.Err != nil {
		t.Fatalf("final state write: %v", saved.Err)
	}
	next, cmd = next.(Model).Update(saved)
	if cmd == nil {
		return next.(Model), false
	}
	_, quit := cmd().(tea.QuitMsg)
	return next.(Model), quit
}

// The binding table must have no key bound twice in one context, no key that
// hides quit or help where they are promised (unless the binding declares
// it), and no shortcut a Korean input source cannot type.
func TestKeymapHasNoConflicts(t *testing.T) {
	// J has g as its Korean-reachable alias; the kit judges reachability per
	// action, so that alone keeps J out of the report.
	for _, c := range defaultKeymap().Conflicts(stacks...) {
		t.Error(c)
	}
}

// Every key the help page lists must run the action it is listed under in
// that context, and every action must be in the help page.
func TestEveryHelpEntryIsReachable(t *testing.T) {
	km := defaultKeymap()
	listed := map[string]bool{}
	for _, sec := range (Model{}).helpSections() {
		for _, e := range sec.Entries {
			listed[e.Action] = true
			stack := stacks[0]
			if e.Group == "Detail" {
				stack = stacks[1]
			}
			for _, k := range e.Keys {
				b, ok := km.Resolve(stack, keyMsg(k), keys.Opts{})
				if !ok || b.Action != e.Action {
					t.Errorf("help lists %q for %s, but it resolves to %q (ok=%v)", k, e.Action, b.Action, ok)
				}
			}
		}
	}
	for _, b := range km.Bindings() {
		if b.Ctx != ctxHelp && !listed[b.Action] {
			t.Errorf("action %s is bound but missing from help", b.Action)
		}
	}
}

// t is one action whose label says which way it goes, in the hint bar and
// in the help page alike.
func TestGroupLabelFollowsTheBoard(t *testing.T) {
	shared := []store.Item{
		item("s1", "/src/a", "a", store.StateWorking, ""),
		item("s2", "/src/a", "b", store.StateWorking, ""),
	}
	m := board(140, 20, shared...)
	if h := m.renderHints(); !strings.Contains(h, "t group") {
		t.Errorf("ungrouped footer = %q", h)
	}
	m = press(m, "t")
	if !m.group {
		t.Fatal("t did not group")
	}
	if h := m.renderHints(); !strings.Contains(h, "t ungroup") {
		t.Errorf("grouped footer = %q", h)
	}
	var label string
	for _, sec := range m.helpSections() {
		for _, e := range sec.Entries {
			if e.Action == actGroup {
				label = e.Help
			}
		}
	}
	if label != "ungroup" {
		t.Errorf("help labels t %q while grouped, want ungroup", label)
	}
	if m = press(m, "t"); m.group {
		t.Error("t did not ungroup")
	}
}

// g is jump's alias because J cannot be typed under a Korean input source.
func TestJumpHasAKeyReachableUnderKorean(t *testing.T) {
	t.Setenv("TMUX", "")
	m := board(140, 20, item("s1", "/src/a", "a", store.StateWaiting, "idle_prompt"))
	m.setRow(0)
	_, cmd := m.handleKey(keyMsg("ㅎ")) // physical g
	if cmd == nil {
		t.Fatal("ㅎ (physical g) did not jump")
	}
	if _, ok := cmd().(jumpMsg); !ok {
		t.Fatal("ㅎ (physical g) returned something other than a jump")
	}
}

// A user file rebinds by action name; a typo in it is reported, not fatal.
func TestKeyOverridesFile(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(cfg, "brd"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		if err := os.WriteFile(keys.ConfigPath(appName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("refresh: [\"ctrl+r\"]\n")
	m := New(nil)
	if b, ok := m.keymap().Resolve(m.active(), tea.KeyMsg{Type: tea.KeyCtrlR}, m.keyOpts()); !ok || b.Action != actRefresh {
		t.Errorf("ctrl+r resolved to %q (ok=%v), want refresh", b.Action, ok)
	}
	if _, ok := m.keymap().Resolve(m.active(), keyMsg("r"), m.keyOpts()); ok {
		t.Error("r still bound after it was overridden")
	}

	write("refesh: [\"x\"]\n")
	m = New(nil)
	items := m.toasts.Items()
	if len(items) != 1 || items[0].Level != toast.Error || !strings.Contains(items[0].Text, "refesh") {
		t.Errorf("unknown action not reported as an error toast: %+v", items)
	}
	if b, ok := m.keymap().Resolve(m.active(), keyMsg("r"), m.keyOpts()); !ok || b.Action != actRefresh {
		t.Error("a bad override file did not fall back to the defaults")
	}
}
