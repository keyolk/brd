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
	Ticket      string
	Branch      string
	// Live is set by the TUI from tmux, not by a hook and not from the
	// database: no hook can report that a session still exists, only what it
	// last did. See internal/tui/liveness.go.
	Live       bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
	StateSince time.Time
	Children   []Child
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
//
// Idle and Ended are separate columns even though both mean "no turn running",
// because the difference is whether you can still type into it. Collapsing
// them was the original mistake: `Stop` fires when a *turn* ends, so a session
// sitting at a prompt landed in "Done" and read as finished work. Measured
// against tmux, 10 of 26 live sessions were being shown that way.
func (i Item) Column() string {
	switch i.State {
	case StateWaiting:
		return "Blocked"
	case StateWorking:
		return "Working"
	case StateBackground:
		return "Background"
	case StateEnded:
		// Not a column: an ended session is off the board entirely. See
		// Model.inColumn.
		return ""
	}
	return "Idle"
}

// Age is how long the item has held its current state.
func (i Item) Age() time.Duration { return time.Since(i.StateSince) }

// GroupKey is what the board groups this item under.
//
// A ticket when there is one, the repository otherwise. Grouping by ticket
// alone was almost inert: on a live board of 19 items only 2 carried a ticket,
// and those two were different tickets, so the grouped view was identical to
// the ungrouped one. Nearly every item has a repository, and several sessions
// in one repository is the shape the board actually holds.
//
// The prefix keeps the two kinds sortable and distinguishable without a second
// field: a ticket sorts before a repository, because a named piece of work is
// more specific than the directory it happens in.
func (i Item) GroupKey() string {
	if i.Ticket != "" {
		return "1" + i.Ticket
	}
	if i.Repo != "" {
		return "2" + i.Repo
	}
	// Nothing to group by. Sorts last, so unattributable items do not split
	// a run of items that do belong together.
	return "3"
}

// GroupLabel is the header shown above a group, or "" for items with nothing
// to group by.
func (i Item) GroupLabel() string {
	if i.Ticket != "" {
		return i.Ticket
	}
	return i.Repo
}

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
	Ticket      *string
	Branch      *string
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
			                   last_message, model, turns, created_at, updated_at,
			                   state_since, ticket, branch)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			u.ID, deref(u.Repo, ""), deref(u.Cwd, ""), deref(u.Title, ""),
			deref(u.Subject, ""), state, deref(u.BlockedOn, ""),
			deref(u.LastMessage, ""), deref(u.Model, ""), boolToInt(u.BumpTurn),
			now, now, now, deref(u.Ticket, ""), deref(u.Branch, ""))
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
		add("ticket", u.Ticket)
		add("branch", u.Branch)
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
		       model, turns, created_at, updated_at, state_since, ticket, branch
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
			&created, &updated, &since, &it.Ticket, &it.Branch); err != nil {
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

// Pull is a pull request the board knows about, written by the TUI's poller.
type Pull struct {
	Repo      string // owner/name on GitHub, not a local path
	Number    int
	Ticket    string
	Branch    string
	Title     string
	State     string // OPEN | MERGED | CLOSED
	Review    string // APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED
	Checks    string // passing | failing | pending | none
	URL       string
	UpdatedAt time.Time
	FetchedAt time.Time
}

// Attention reports whether this PR is the user's move.
//
// Merged and closed PRs are history. Among open ones, a failing check or a
// requested change is something only the author can clear — an approval that
// has not been merged is too, but it is good news, so it is not lumped in
// with the two that are not.
func (p Pull) Attention() bool {
	if p.State != "OPEN" {
		return false
	}
	return p.Checks == "failing" || p.Review == "CHANGES_REQUESTED"
}

// PutPull records or refreshes one pull request.
func (d *DB) PutPull(p Pull) error {
	_, err := d.sql.Exec(`
		INSERT INTO pulls (repo, number, ticket, branch, title, state, review,
		                   checks, url, updated_at, fetched_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(repo, number) DO UPDATE SET
		  ticket = excluded.ticket, branch = excluded.branch,
		  title = excluded.title, state = excluded.state,
		  review = excluded.review, checks = excluded.checks,
		  url = excluded.url, updated_at = excluded.updated_at,
		  fetched_at = excluded.fetched_at`,
		p.Repo, p.Number, p.Ticket, p.Branch, p.Title, p.State, p.Review,
		p.Checks, p.URL, p.UpdatedAt.Unix(), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("brd: put pull: %w", err)
	}
	return nil
}

// Pulls returns every known pull request, newest first.
//
// The board joins these to items in memory rather than in SQL: an item matches
// by ticket when it has one and by branch otherwise, and expressing that
// either-or in a query costs more than it saves at this size.
func (d *DB) Pulls() ([]Pull, error) {
	rows, err := d.sql.Query(`
		SELECT repo, number, ticket, branch, title, state, review, checks, url,
		       updated_at, fetched_at
		FROM pulls ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("brd: query pulls: %w", err)
	}
	defer rows.Close()
	var out []Pull
	for rows.Next() {
		var p Pull
		var updated, fetched int64
		if err := rows.Scan(&p.Repo, &p.Number, &p.Ticket, &p.Branch, &p.Title,
			&p.State, &p.Review, &p.Checks, &p.URL, &updated, &fetched); err != nil {
			return nil, fmt.Errorf("brd: scan pull: %w", err)
		}
		p.UpdatedAt = time.Unix(updated, 0)
		p.FetchedAt = time.Unix(fetched, 0)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("brd: iterate pulls: %w", err)
	}
	return out, nil
}
