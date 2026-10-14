package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/prefs"
	"github.com/keyolk/tuikit/toast"

	"github.com/keyolk/brd/internal/store"
)

// Grouping, the focused column and the selected session survive a restart,
// and the selection is restored by ID, not position.
func TestUIStateSurvivesARestart(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a := item("s1", "/src/a", "a", store.StateWorking, "")
	b := item("s2", "/src/b", "b", store.StateWorking, "")

	m := New(nil)
	m.items, m.w, m.h = []store.Item{a, b}, 140, 20
	for _, k := range []string{"l", "j", "t"} {
		next, _ := m.Update(keyMsg(k))
		m = next.(Model)
	}
	if !m.group || m.col != 1 || m.sel != "s2" {
		t.Fatalf("setup: group=%v col=%d sel=%q", m.group, m.col, m.sel)
	}
	if _, quit := quitAfterSave(t, m, keyMsg("q")); !quit {
		t.Fatal("q did not quit after saving")
	}

	r := New(nil)
	if !r.group || r.col != 1 || r.sel != "s2" {
		t.Fatalf("restored group=%v col=%d sel=%q, want true 1 s2", r.group, r.col, r.sel)
	}
	// s2 now sorts first; the cursor must follow it, not stay on row 1.
	next, _ := r.Update(loadedMsg{items: []store.Item{b, a}})
	if sel, ok := next.(Model).selected(); !ok || sel.ID != "s2" {
		t.Errorf("restored selection landed on %q (ok=%v), want s2", sel.ID, ok)
	}
}

// A change arms one debounced write; a newer change makes the older tick a
// no-op, so holding j writes once.
func TestStateWritesAreDebounced(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := board(140, 20, item("s1", "/src/a", "a", store.StateWorking, ""))
	m.saver = prefs.NewSaver[uiState](appName, 0)
	next, first := m.Update(keyMsg("l"))
	m = next.(Model)
	if first == nil {
		t.Fatal("changing column armed no save")
	}
	stale := m.saver.TickMsg()
	next, _ = m.Update(keyMsg("l"))
	m = next.(Model)

	next, cmd := m.Update(stale)
	if cmd != nil {
		t.Error("a superseded debounce tick still wrote")
	}
	m = next.(Model)
	_, cmd = m.Update(m.saver.TickMsg())
	if cmd == nil {
		t.Fatal("the latest debounce tick did not write")
	}
	if saved, ok := cmd().(prefs.Saved); !ok || saved.Err != nil {
		t.Fatalf("write result = %#v", saved)
	}
	data, err := os.ReadFile(prefs.Path(appName))
	if err != nil || !strings.Contains(string(data), `"column": 2`) {
		t.Errorf("ui.json = %s (err %v), want column 2", data, err)
	}
}

// A corrupt or out-of-range state file never stops the board.
func TestBadStateFileFallsBackToDefaults(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(strings.TrimSuffix(prefs.Path(appName), "/ui.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prefs.Path(appName), []byte(`{"column": 9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if m := New(nil); m.col != 0 {
		t.Errorf("column 9 restored as %d, want clamped to 0", m.col)
	}
	if err := os.WriteFile(prefs.Path(appName), []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(nil)
	if items := m.toasts.Items(); len(items) != 1 || !strings.Contains(items[0].Text, "UI state ignored") {
		t.Errorf("corrupt state not reported: %+v", items)
	}
}

// `?` draws the help over the board, derived from the table; esc closes it
// and toasts wait while it is up.
func TestHelpOverlayKeepsTheBoard(t *testing.T) {
	m := board(140, 30, item("s1", "/src/a", "alpha", store.StateWaiting, "idle_prompt"))
	m.notify("jump", toast.Info, "hello")
	next, _ := m.Update(keyMsg("?"))
	m = next.(Model)
	out := m.View()
	for _, want := range []string{"alpha", "next column", "jump", "space"} {
		if !strings.Contains(out, want) {
			t.Errorf("help view lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hello") {
		t.Error("toast drawn while the help overlay is up")
	}
	for i, line := range strings.Split(out, "\n") {
		if w := lipglossWidth(line); w > 140 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if m.help || !strings.Contains(m.View(), "hello") {
		t.Error("esc did not close help and release the toast")
	}
}

// The hint bar in the detail pane offers esc and does not claim q quits,
// because there q closes the pane.
func TestDetailFooterIsHonestAboutQ(t *testing.T) {
	m := board(140, 20, item("s1", "/src/a", "a", store.StateWaiting, "idle_prompt"))
	m.setRow(0)
	m.detail = true
	h := m.renderHints()
	if !strings.Contains(h, "esc close") || strings.Contains(h, "q quit") || !strings.Contains(h, "ctrl+c quit") {
		t.Errorf("detail footer = %q", h)
	}
	if strings.Contains(h, "move") {
		t.Errorf("detail footer offers navigation: %q", h)
	}
}

// Holding j at the end of the help page does not grow the offset past the
// last page, so one k moves the page back at once.
func TestHelpOffsetIsClamped(t *testing.T) {
	m := board(80, 12)
	m = press(m, "?")
	for range 50 {
		m = press(m, "j")
	}
	last := m.helpOff
	if last <= 0 || last >= 50 {
		t.Fatalf("offset after 50 j = %d, want clamped to the last page", last)
	}
	if m = press(m, "k"); m.helpOff != last-1 {
		t.Errorf("k moved the offset to %d, want %d", m.helpOff, last-1)
	}
}

// Only the final flush quits: an earlier debounced write landing while the
// quit is in flight must not end the program before the latest state is on
// disk.
func TestOnlyTheFinalWriteQuits(t *testing.T) {
	m := board(140, 20)
	m.quitting = true
	if _, cmd := m.Update(prefs.Saved{App: appName}); cmd != nil {
		t.Error("a non-final write ended the board")
	}
}
