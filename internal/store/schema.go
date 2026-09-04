// Package store is brd's durable board state: one SQLite file that every
// hook invocation writes to and the TUI reads from.
//
// Why SQLite and not a JSON file per item: hooks fire from every Claude Code
// session on the machine at once, and a turn boundary in one session has no
// coordination with another. A directory of JSON files needs a lock protocol
// invented here; SQLite in WAL mode already has one, and `modernc.org/sqlite`
// keeps the binary CGO-free like the rest of these tools.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// State is what a work item is doing right now. These are *derived* from hook
// events, never written by the agent, so the board cannot lie about them.
//
// The distinction that matters most is Background vs Done. Both arrive on the
// same `Stop` event; the only thing separating "this session finished" from
// "this session is parked waiting for its own background work to wake it up"
// is whether `background_tasks` came back empty. Collapsing them would make
// every long-running agent look finished.
const (
	StateWorking    = "working"    // a turn is in flight
	StateWaiting    = "waiting"    // blocked on a human — permission, input
	StateBackground = "background" // turn ended, but its own tasks still run
	StateDone       = "done"       // turn ended clean
	StateEnded      = "ended"      // session closed
)

// Child kinds. The first seven mirror the `type` labels Claude Code puts in
// `background_tasks[]` on Stop; `task` is a TaskCreate/TaskUpdate task, which
// only appears in sessions that opted the Task tools back in.
const (
	KindSubagent = "subagent"
	KindShell    = "shell"
	KindMonitor  = "monitor"
	KindWorkflow = "workflow"
	KindTeammate = "teammate"
	KindCloud    = "cloud session"
	KindMCPTask  = "MCP task"
	KindCron     = "cron"
	KindTask     = "task"
)

const schema = `
CREATE TABLE IF NOT EXISTS items (
  id           TEXT PRIMARY KEY,   -- session_id
  repo         TEXT NOT NULL,      -- git root of cwd, or cwd when not a repo
  cwd          TEXT NOT NULL,
  title        TEXT NOT NULL,      -- session title, else first prompt
  subject      TEXT NOT NULL,      -- most recent prompt
  state        TEXT NOT NULL,
  blocked_on   TEXT NOT NULL,      -- notification_type while waiting
  last_message TEXT NOT NULL,
  model        TEXT NOT NULL,
  turns        INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  state_since  INTEGER NOT NULL    -- when the current state was entered
);

CREATE INDEX IF NOT EXISTS items_repo_state ON items(repo, state);
CREATE INDEX IF NOT EXISTS items_updated ON items(updated_at DESC);

-- Live children of an item: background tasks and subagents.
--
-- background_tasks[] on Stop is a *complete* snapshot of what is in flight,
-- so those rows are replaced wholesale on every Stop rather than merged —
-- a task that vanished from the array has finished, and merging would leave
-- it on the board forever. Subagent rows are keyed the same way but driven
-- by SubagentStart/SubagentStop, which do carry explicit start and end.
CREATE TABLE IF NOT EXISTS children (
  item_id    TEXT NOT NULL,
  kind       TEXT NOT NULL,
  ref        TEXT NOT NULL,       -- agent_id, task id
  label      TEXT NOT NULL,
  state      TEXT NOT NULL,
  detail     TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (item_id, kind, ref)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS events (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  item_id TEXT NOT NULL,
  at      INTEGER NOT NULL,
  event   TEXT NOT NULL,
  detail  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS events_item ON events(item_id, at DESC);
`

// DB wraps the board database.
type DB struct{ sql *sql.DB }

// DefaultPath is where the board lives. XDG-ish, with BRD_DB as the override
// the tests and a second board need.
func DefaultPath() string {
	if p := os.Getenv("BRD_DB"); p != "" {
		return p
	}
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "brd", "board.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "board.db"
	}
	return filepath.Join(home, ".local", "share", "brd", "board.db")
}

// Open prepares the board at path, creating it and its parent if needed.
//
// The pragmas are not optional. Hooks from unrelated sessions write
// concurrently, and without WAL plus a busy timeout a second writer gets
// SQLITE_BUSY immediately — which in a hook means a lost state transition,
// the one failure that makes the whole board untrustworthy.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("brd: create db dir: %w", err)
	}
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("brd: open db: %w", err)
	}
	if _, err := sqlDB.Exec(schema); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("brd: migrate: %w", err)
	}
	return &DB{sql: sqlDB}, nil
}

// Close releases the database.
func (d *DB) Close() error { return d.sql.Close() }
