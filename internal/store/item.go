package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Item is one unit of work on the board: a Claude Code session and everything
// it has in flight.
//
// The session is the unit, not the task, because a session is the only thing
// every hook event can be attributed to. `~/.claude/tasks/<session>/` is
// session-scoped too, so nothing in the harness offers an identity that
// outlives a session — an item that spans days is a thing brd would have to
// invent, and inventing it means asking the agent to maintain it, which is
// the dependency this design exists to avoid.
type Item struct {
	ID          string
	Repo        string
	Cwd         string
	Title       string
	Subject     string
	State       string
	BlockedOn   string
	LastMessage string
	Model       string
	Turns       int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	StateSince  time.Time
	Children    []Child
}

// Child is a live piece of work owned by an item.
type Child struct {
	Kind      string
	Ref       string
	Label     string
	State     string
	Detail    string
	UpdatedAt time.Time
}

// Column is the board column an item belongs in. Derived, never stored: the
// column is a function of state, so it can never disagree with it.
func (i Item) Column() string {
	switch i.State {
	case StateWaiting:
		return "Blocked"
	case StateWorking:
		return "Working"
	case StateBackground:
		return "Background"
	case StateDone, StateEnded:
		return "Done"
	}
	return "Done"
}

// Age is how long the item has held its current state.
func (i Item) Age() time.Duration { return time.Since(i.StateSince) }

// Upsert creates or refreshes an item, applying only the fields a given hook
// event actually knows.
//
// Every field is passed as a pointer so that "the hook did not carry this"
// and "the hook carried an empty value" stay distinguishable. Without that,
// a Notification hook — which knows the state and nothing else — would blank
// out the title and subject that UserPromptSubmit established.
type Upsert struct {
	ID          string
	Repo        *string
	Cwd         *string
	Title       *string
	Subject     *string
	State       *string
	BlockedOn   *string
	LastMessage *string
	Model       *string
	BumpTurn    bool
}

// Apply writes the upsert. It returns the state the item ended up in.
func (d *DB) Apply(u Upsert) (string, error) {
	if u.ID == "" {
		return "", fmt.Errorf("brd: upsert without session id")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return "", fmt.Errorf("brd: begin: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	var prevState string
	err = tx.QueryRow(`SELECT state FROM items WHERE id = ?`, u.ID).Scan(&prevState)
	switch {
	case err == sql.ErrNoRows:
		state := deref(u.State, StateWorking)
		_, err = tx.Exec(`
			INSERT INTO items (id, repo, cwd, title, subject, state, blocked_on,
			                   last_message, model, turns, created_at, updated_at, state_since)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			u.ID, deref(u.Repo, ""), deref(u.Cwd, ""), deref(u.Title, ""),
			deref(u.Subject, ""), state, deref(u.BlockedOn, ""),
			deref(u.LastMessage, ""), deref(u.Model, ""), boolToInt(u.BumpTurn),
			now, now, now)
		if err != nil {
			return "", fmt.Errorf("brd: insert item: %w", err)
		}
		prevState = state
	case err != nil:
		return "", fmt.Errorf("brd: read item: %w", err)
	default:
		sets := []string{"updated_at = ?"}
		args := []any{now}
		add := func(col string, v *string) {
			if v != nil {
				sets = append(sets, col+" = ?")
				args = append(args, *v)
			}
		}
		add("repo", u.Repo)
		add("cwd", u.Cwd)
		add("title", u.Title)
		add("subject", u.Subject)
		add("blocked_on", u.BlockedOn)
		add("last_message", u.LastMessage)
		add("model", u.Model)
		if u.State != nil {
			sets = append(sets, "state = ?")
			args = append(args, *u.State)
			// state_since only moves on a real transition, so "waiting for
			// 40 minutes" survives the turns that keep re-asserting waiting.
			if *u.State != prevState {
				sets = append(sets, "state_since = ?")
				args = append(args, now)
			}
			prevState = *u.State
		}
		if u.BumpTurn {
			sets = append(sets, "turns = turns + 1")
		}
		args = append(args, u.ID)
		_, err = tx.Exec(`UPDATE items SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
		if err != nil {
			return "", fmt.Errorf("brd: update item: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("brd: commit: %w", err)
	}
	return prevState, nil
}

// ReplaceBackground swaps in the complete set of background tasks reported by
// a Stop event.
//
// Wholesale replacement, not a merge: `background_tasks[]` is a snapshot of
// everything in flight, so a task missing from it has finished. Merging would
// pin finished shell jobs to the board permanently. Subagent rows are exempt
// because SubagentStart/Stop track them with explicit edges and a subagent
// that is mid-flight at a turn boundary would otherwise be dropped and
// re-added, resetting its age.
func (d *DB) ReplaceBackground(itemID string, kids []Child) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("brd: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`DELETE FROM children WHERE item_id = ? AND kind != ?`, itemID, KindSubagent,
	); err != nil {
		return fmt.Errorf("brd: clear children: %w", err)
	}
	now := time.Now().Unix()
	for _, c := range kids {
		if c.Kind == KindSubagent {
			// Reported by Stop too, but SubagentStart/Stop owns these rows.
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO children (item_id, kind, ref, label, state, detail, updated_at)
			VALUES (?,?,?,?,?,?,?)
			ON CONFLICT(item_id, kind, ref) DO UPDATE SET
			  label = excluded.label, state = excluded.state,
			  detail = excluded.detail, updated_at = excluded.updated_at`,
			itemID, c.Kind, c.Ref, c.Label, c.State, c.Detail, now,
		); err != nil {
			return fmt.Errorf("brd: insert child: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("brd: commit children: %w", err)
	}
	return nil
}

// PutChild records or updates one child.
func (d *DB) PutChild(itemID string, c Child) error {
	_, err := d.sql.Exec(`
		INSERT INTO children (item_id, kind, ref, label, state, detail, updated_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(item_id, kind, ref) DO UPDATE SET
		  label = excluded.label, state = excluded.state,
		  detail = excluded.detail, updated_at = excluded.updated_at`,
		itemID, c.Kind, c.Ref, c.Label, c.State, c.Detail, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("brd: put child: %w", err)
	}
	return nil
}

// DropChild removes one child.
func (d *DB) DropChild(itemID, kind, ref string) error {
	_, err := d.sql.Exec(
		`DELETE FROM children WHERE item_id = ? AND kind = ? AND ref = ?`,
		itemID, kind, ref)
	if err != nil {
		return fmt.Errorf("brd: drop child: %w", err)
	}
	return nil
}

// LogEvent appends to the item's audit trail.
func (d *DB) LogEvent(itemID, event, detail string) error {
	_, err := d.sql.Exec(
		`INSERT INTO events (item_id, at, event, detail) VALUES (?,?,?,?)`,
		itemID, time.Now().Unix(), event, detail)
	if err != nil {
		return fmt.Errorf("brd: log event: %w", err)
	}
	return nil
}

// Items returns the board, most recently updated first, with children
// attached. Items untouched for longer than maxAge are omitted so a board
// left running for a week does not accumulate every session ever opened.
func (d *DB) Items(maxAge time.Duration) ([]Item, error) {
	cutoff := time.Now().Add(-maxAge).Unix()
	rows, err := d.sql.Query(`
		SELECT id, repo, cwd, title, subject, state, blocked_on, last_message,
		       model, turns, created_at, updated_at, state_since
		FROM items WHERE updated_at >= ? ORDER BY updated_at DESC`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("brd: query items: %w", err)
	}
	defer rows.Close()

	var items []Item
	byID := map[string]int{}
	for rows.Next() {
		var it Item
		var created, updated, since int64
		if err := rows.Scan(&it.ID, &it.Repo, &it.Cwd, &it.Title, &it.Subject,
			&it.State, &it.BlockedOn, &it.LastMessage, &it.Model, &it.Turns,
			&created, &updated, &since); err != nil {
			return nil, fmt.Errorf("brd: scan item: %w", err)
		}
		it.CreatedAt = time.Unix(created, 0)
		it.UpdatedAt = time.Unix(updated, 0)
		it.StateSince = time.Unix(since, 0)
		byID[it.ID] = len(items)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("brd: iterate items: %w", err)
	}
	if len(items) == 0 {
		return nil, nil
	}

	kidRows, err := d.sql.Query(`
		SELECT item_id, kind, ref, label, state, detail, updated_at
		FROM children ORDER BY kind, ref`)
	if err != nil {
		return nil, fmt.Errorf("brd: query children: %w", err)
	}
	defer kidRows.Close()
	for kidRows.Next() {
		var itemID string
		var c Child
		var updated int64
		if err := kidRows.Scan(&itemID, &c.Kind, &c.Ref, &c.Label, &c.State,
			&c.Detail, &updated); err != nil {
			return nil, fmt.Errorf("brd: scan child: %w", err)
		}
		c.UpdatedAt = time.Unix(updated, 0)
		if idx, ok := byID[itemID]; ok {
			items[idx].Children = append(items[idx].Children, c)
		}
	}
	if err := kidRows.Err(); err != nil {
		return nil, fmt.Errorf("brd: iterate children: %w", err)
	}
	return items, nil
}

// EndSession marks a session closed and clears its children — nothing it had
// in flight outlives the process.
func (d *DB) EndSession(id string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("brd: begin: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.Exec(`
		UPDATE items SET state = ?, blocked_on = '', updated_at = ?,
		  state_since = CASE WHEN state != ? THEN ? ELSE state_since END
		WHERE id = ?`, StateEnded, now, StateEnded, now, id); err != nil {
		return fmt.Errorf("brd: end session: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM children WHERE item_id = ?`, id); err != nil {
		return fmt.Errorf("brd: clear children: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("brd: commit end: %w", err)
	}
	return nil
}

func deref(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// HasTitle reports whether the item already carries a title. The first prompt
// of a session names it; later prompts must not rename it, or the board loses
// the label the work is remembered by halfway through.
func (d *DB) HasTitle(id string) bool {
	var title string
	err := d.sql.QueryRow(`SELECT title FROM items WHERE id = ?`, id).Scan(&title)
	return err == nil && title != ""
}
