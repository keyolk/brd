package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/brd/internal/store"
	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/link"
	"github.com/keyolk/tuikit/selection"
	"github.com/keyolk/tuikit/tuitest"
)

const (
	pr90 = "https://github.com/keyolk/ghx/pull/90"
	pr91 = "https://github.com/keyolk/ghx/pull/91"
)

func linked(url, label string) string {
	return "\x1b]8;;" + url + "\x1b\\" + label + "\x1b]8;;\x1b\\"
}

// stubLauncher records what link.Open hands the browser.
func stubLauncher(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old := link.Launcher
	link.Launcher = func(u string) error {
		got = append(got, u)
		return nil
	}
	t.Cleanup(func() { link.Launcher = old })
	return &got
}

// linkBoard: alpha has two PRs (#90 failing checks, so the badge names it),
// beta one with a javascript: URL, gamma none and a last message with a URL.
func linkBoard() Model {
	m := mouseBoard()
	m.items[0].Branch = "alpha"
	m.items[1].Branch = "beta"
	m.items[2].LastMessage = "pushed; see https://example.com/run/7 for the log."
	m.pulls = []store.Pull{
		{Branch: "alpha", Number: 91, State: "OPEN", Title: "second change", URL: pr91},
		{Branch: "alpha", Number: 90, State: "OPEN", Checks: "failing", Title: "first change", URL: pr90},
		{Branch: "beta", Number: 5, State: "OPEN", Title: "bad url", URL: "javascript:alert(1)"},
	}
	return m
}

// pressKey runs a key through Update and feeds back the messages its command
// produced, as the Bubble Tea loop would.
func pressKey(t *testing.T, m Model, k string) (Model, []tea.Msg) {
	t.Helper()
	m, cmd := send(m, keyMsg(k))
	msgs := tuitest.Drain(cmd, 0)
	for _, msg := range msgs {
		m, _ = send(m, msg)
	}
	return m, msgs
}

func TestPRBadgeLinksToThePRItNames(t *testing.T) {
	m := linkBoard()
	if !strings.Contains(m.View(), linked(pr90, "#90 checks failing")) {
		t.Fatalf("the badge should link to the PR it names:\n%q", m.View())
	}
	if strings.Contains(m.View(), "javascript:") {
		t.Fatal("a javascript: URL from the data was linked")
	}
}

func TestDetailPRTitlesAreLinks(t *testing.T) {
	m := linkBoard()
	x, y := at(t, m, "alpha task")
	m = click(click(m, x, y), x, y)
	if !m.detail {
		t.Fatal("setup: detail should be open")
	}
	for url, title := range map[string]string{pr90: "first change", pr91: "second change"} {
		if !strings.Contains(m.View(), linked(url, title)) {
			t.Fatalf("detail: %q should link to %s:\n%q", title, url, m.View())
		}
	}
}

func TestOOpensTheBadgePRFirstAndSaysHowManyLinks(t *testing.T) {
	opened := stubLauncher(t)
	m := linkBoard()
	x, y := at(t, m, "alpha task")
	m = click(m, x, y)
	m, msgs := pressKey(t, m, "o")
	if len(*opened) != 1 || (*opened)[0] != pr90 {
		t.Fatalf("o opened %q, want only %s (the PR the badge names)", *opened, pr90)
	}
	var got link.OpenedMsg
	for _, msg := range msgs {
		if o, ok := msg.(link.OpenedMsg); ok {
			got = o
		}
	}
	if got.URL != pr90 || got.Err != nil {
		t.Fatalf("o reported %#v", msgs)
	}
	screen := ansi.Strip(m.View())
	if !strings.Contains(screen, "2 links on this card") || !strings.Contains(screen, "opened "+pr90) {
		t.Fatalf("o should say how many links there were and what it opened:\n%s", screen)
	}
}

func TestOOpensAURLFromTheLastMessage(t *testing.T) {
	opened := stubLauncher(t)
	m := linkBoard()
	x, y := at(t, m, "gamma task")
	m = click(m, x, y)
	m, msgs := pressKey(t, m, "o")
	want := "https://example.com/run/7"
	if len(*opened) != 1 || (*opened)[0] != want {
		t.Fatalf("o opened %q, want %q (sentence period dropped)", *opened, want)
	}
	if len(msgs) != 1 {
		t.Fatalf("one URL: o should return exactly one OpenedMsg, got %#v", msgs)
	}
	if _, ok := msgs[0].(link.OpenedMsg); !ok {
		t.Fatalf("o returned %#v", msgs[0])
	}
}

func TestOOnACardWithoutALinkOpensNothingAndSaysSo(t *testing.T) {
	opened := stubLauncher(t)
	m := linkBoard()
	x, y := at(t, m, "idle task")
	m = click(m, x, y)
	m, _ = pressKey(t, m, "o")
	if len(*opened) != 0 {
		t.Fatalf("o on a card without a URL opened %q", *opened)
	}
	if !strings.Contains(ansi.Strip(m.View()), "no link on this card") {
		t.Fatal("o on a card without a URL should say so")
	}
}

func TestOSkipsAJavascriptURL(t *testing.T) {
	opened := stubLauncher(t)
	m := linkBoard()
	x, y := at(t, m, "beta task")
	m = click(m, x, y)
	m, _ = pressKey(t, m, "o")
	if len(*opened) != 0 {
		t.Fatalf("a javascript: URL reached the launcher: %q", *opened)
	}
	// Not "open failed": a URL that is not http(s) is not a link at all.
	if !strings.Contains(ansi.Strip(m.View()), "no link on this card") {
		t.Fatalf("a card whose only URL is javascript: has no link:\n%s", ansi.Strip(m.View()))
	}
}

func TestOpenLinkIsInHelpAndWorksInTheDetailPane(t *testing.T) {
	m := linkBoard()
	help := strings.Join(keys.HelpLines(m.helpSections(), 60, m.th.HelpStyle()), "\n")
	if !strings.Contains(ansi.Strip(help), "open link") {
		t.Fatalf("help does not list open link:\n%s", help)
	}
	opened := stubLauncher(t)
	x, y := at(t, m, "alpha task")
	m = click(click(m, x, y), x, y)
	if !m.detail {
		t.Fatal("setup: detail should be open")
	}
	pressKey(t, m, "o")
	if len(*opened) != 1 || (*opened)[0] != pr90 {
		t.Fatalf("o in the detail pane opened %q", *opened)
	}
}

// --- selection v2 ---

// helpBoard opens help on a screen too short for it, so it scrolls.
func helpBoard(t *testing.T) Model {
	t.Helper()
	m := selBoard()
	m.h = 14
	return press(m, "?")
}

// The help offset is set from the kit's Offset through the help's own clamp.
// A stored offset past the last page (restored state, a taller page) must
// not swallow a drag past the top: the help still scrolls up one line.
func TestDragPastTheTopOfAnOverscrolledHelpScrollsUp(t *testing.T) {
	m := helpBoard(t)
	m.helpOff = 1000
	// On the last page "App" is near the top of the box; scrolling up moves
	// it down one row.
	_, boxTop := at(t, m, "╭")
	first := "App"
	x, y := at(t, m, first)
	m, _ = send(m, mpress(x, y))
	m, _ = send(m, mmotion(x, boxTop)) // onto the border, above the content
	_, yAfter := at(t, m, first)
	if yAfter != y+1 {
		t.Fatalf("%q moved from row %d to %d; a drag past the top scrolls up one line", first, y, yAfter)
	}
}

func TestDragRestingPastTheHelpBottomKeepsScrolling(t *testing.T) {
	old := selection.AutoScrollInterval
	selection.AutoScrollInterval = time.Millisecond
	t.Cleanup(func() { selection.AutoScrollInterval = old })
	m := helpBoard(t)
	x, y := at(t, m, "Act")
	_, bottom := at(t, m, "╰")
	m, _ = send(m, mpress(x, y))
	m, cmd := send(m, mmotion(x, bottom))
	if m.helpOff != 1 {
		t.Fatalf("setup: helpOff = %d after one motion past the bottom", m.helpOff)
	}
	for i := range 2 {
		var tick tea.Msg
		for _, msg := range tuitest.Drain(cmd, 200*time.Millisecond) {
			if _, ok := msg.(selection.AutoScrollMsg); ok {
				tick = msg
			}
		}
		if tick == nil {
			t.Fatalf("tick %d: a drag resting past the edge should schedule an AutoScrollMsg", i)
		}
		m, cmd = send(m, tick)
		if m.helpOff != i+2 {
			t.Fatalf("tick %d: helpOff = %d, want %d", i, m.helpOff, i+2)
		}
	}
}

func TestNoColorKeepsTheDragHighlight(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := selBoard()
	x, y := at(t, m, "alpha task")
	m, _ = send(m, mpress(x, y))
	m, _ = send(m, mmotion(x+4, y))
	m, _ = send(m, mrelease(x+4, y))
	if got := tuitest.Reversed(m.View()); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("highlighted %q, want [alpha]", got)
	}
}
