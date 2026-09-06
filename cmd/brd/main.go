// Command brd is a kanban board for Claude Code sessions.
//
// The board is fed entirely by hook events, so `brd hook` is the write path
// and the TUI is a reader. See internal/hook for why nothing here asks the
// agent to report its own progress.
package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/brd/internal/hook"
	"github.com/keyolk/brd/internal/store"
	"github.com/keyolk/brd/internal/tui"
)

const usage = `brd — a kanban board for Claude Code sessions

  brd              launch the board
  brd hook         apply one hook payload from stdin (for settings.json)
  brd install      print the settings.json hook block to add
  brd ls           print the board as text

The board lives at ` + "`$BRD_DB`" + ` or ~/.local/share/brd/board.db.
`

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "hook":
		runHook()
	case "install":
		fmt.Print(installBlock)
	case "ls":
		exitOn(runList())
	case "", "board":
		exitOn(runTUI())
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "brd: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

// runHook never fails loudly. A hook that exits non-zero can block a session
// from stopping (Stop, SubagentStop) or surface an error in the transcript,
// and brd is an observer — it must not interrupt the work it watches.
func runHook() {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		hook.ExitQuiet(fmt.Errorf("read stdin: %w", err))
	}
	ev, err := hook.Read(data)
	if err != nil {
		hook.ExitQuiet(fmt.Errorf("parse payload: %w", err))
	}
	if ev.SessionID == "" {
		hook.ExitQuiet(nil) // nothing to attribute; not an error
	}
	db, err := store.Open(store.DefaultPath())
	if err != nil {
		hook.ExitQuiet(err)
	}
	defer db.Close()
	hook.ExitQuiet(hook.Handle(db, ev))
}

func runTUI() error {
	db, err := store.Open(store.DefaultPath())
	if err != nil {
		return err
	}
	defer db.Close()
	p := tea.NewProgram(tui.New(db), tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func runList() error {
	db, err := store.Open(store.DefaultPath())
	if err != nil {
		return err
	}
	defer db.Close()
	items, err := db.Items(24 * time.Hour)
	if err != nil {
		return err
	}
	items = tui.ApplyLiveness(items)
	if len(items) == 0 {
		fmt.Println("board empty — no session activity in the last 24h")
		return nil
	}
	prs, err := db.Pulls()
	if err != nil {
		return err
	}
	for _, col := range tui.Columns {
		var in []store.Item
		for _, it := range items {
			if it.State == store.StateEnded {
				continue // off the board entirely; see Item.Column
			}
			if it.Column() == col.Title {
				in = append(in, it)
			}
		}
		if len(in) == 0 {
			continue
		}
		// Sorted by group so the text board reads the way the TUI's grouped
		// view does. There is no toggle here: a one-shot listing has nowhere
		// to put a mode, and grouping never loses information.
		sort.SliceStable(in, func(a, b int) bool {
			return in[a].GroupKey() < in[b].GroupKey()
		})

		fmt.Printf("\n%s (%d)\n", col.Title, len(in))
		lastGroup := ""
		for _, it := range in {
			if key := it.GroupKey(); key != lastGroup {
				lastGroup = key
				if label := it.GroupLabel(); label != "" {
					name := label
					if it.Ticket == "" {
						name = tui.RepoName(label)
					}
					fmt.Printf("  %s\n", name)
				}
			}
			// Stamp then age: the clock time is the fixed point you correlate
			// against, the age is the same fact in the form that scans faster.
			when := fmt.Sprintf("%s %s", tui.Stamp(it.StateSince), tui.ShortAge(it.Age()))
			why := tui.BlockedLabel(it.BlockedOn)
			// The same live marker the TUI draws: without it a text board of
			// mostly-running sessions reads as a list of finished ones.
			pulse := "·"
			if it.Live {
				pulse = "●"
			}
			// Rows are indented under their group header rather than
			// carrying a repo column: the heading already names it, and a
			// blank column in its place wasted 22 of them.
			fmt.Printf("  %s %-13s %-18s %s\n",
				pulse, when, why, tui.Label(it))
			for _, c := range it.Children {
				fmt.Printf("%20s└ %s %s\n", "", c.Kind, c.Label)
			}
			if badge, _ := tui.PullBadge(tui.PullsFor(it, prs)); badge != "" {
				fmt.Printf("%20s└ %s\n", "", badge)
			}
		}
	}
	return nil
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "brd: %v\n", err)
		os.Exit(1)
	}
}

// installBlock is the settings.json fragment that wires the board up.
//
// Seven events, each cheap: brd reads stdin, writes one row, exits. No event
// here supports a matcher that would narrow it usefully except Notification,
// and brd filters those in code so the blocking set stays in one place.
const installBlock = `Add to ~/.claude/settings.json (merge into existing "hooks"):

{
  "hooks": {
    "SessionStart":     [{ "hooks": [{ "type": "command", "command": "brd hook" }] }],
    "UserPromptSubmit": [{ "hooks": [{ "type": "command", "command": "brd hook" }] }],
    "Notification":     [{ "hooks": [{ "type": "command", "command": "brd hook" }] }],
    "Stop":             [{ "hooks": [{ "type": "command", "command": "brd hook" }] }],
    "SubagentStart":    [{ "hooks": [{ "type": "command", "command": "brd hook" }] }],
    "SubagentStop":     [{ "hooks": [{ "type": "command", "command": "brd hook" }] }],
    "SessionEnd":       [{ "hooks": [{ "type": "command", "command": "brd hook" }] }]
  }
}
`
