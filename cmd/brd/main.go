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
	if len(items) == 0 {
		fmt.Println("board empty — no session activity in the last 24h")
		return nil
	}
	for _, col := range tui.Columns {
		var in []store.Item
		for _, it := range items {
			if it.Column() == col.Title {
				in = append(in, it)
			}
		}
		if len(in) == 0 {
			continue
		}
		fmt.Printf("\n%s (%d)\n", col.Title, len(in))
		for _, it := range in {
			// The blocked reason is the whole reason to read this column, so
			// it goes on the row itself rather than only in the TUI detail.
			age := tui.ShortAge(it.Age())
			if it.BlockedOn != "" {
				age += " " + it.BlockedOn
			}
			fmt.Printf("  %-26s %-22s %s\n",
				age, tui.RepoName(it.Repo), tui.Label(it))
			for _, c := range it.Children {
				fmt.Printf("            └ %s %s\n", c.Kind, c.Label)
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
