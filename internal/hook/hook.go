// Package hook turns Claude Code hook events into board state.
//
// Every state on the board is derived here, from events the harness pushes on
// its own. Nothing asks the agent to report progress, which is what makes the
// board trustworthy: an agent that never mentions brd still shows up on it,
// correctly, and an agent that is stuck cannot hide by staying quiet.
//
// The event budget is deliberately small. These seven carry everything the
// board needs:
//
//	UserPromptSubmit  → working, and the prompt is the item's subject
//	Notification      → waiting, and *what* it waits on
//	Stop              → done, or background when background_tasks[] is non-empty
//	SubagentStart     → a child appears
//	SubagentStop      → that child resolves, with its final message
//	SessionStart      → identity: repo, model, title
//	SessionEnd        → ended
//
// Hooks must never block a session. Every handler here exits 0 on every
// failure it can reach — an unwritable database is a degraded board, but a
// hook that fails is a session that cannot stop. This mirrors what pigeon's
// Stop hook learned the hard way.
package hook

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/keyolk/brd/internal/store"
)

// Event is the union of the hook payload fields brd reads. Claude Code sends
// one JSON object on stdin per invocation; fields absent for a given event
// simply stay zero.
type Event struct {
	HookEventName string `json:"hook_event_name"`
	SessionID     string `json:"session_id"`
	Cwd           string `json:"cwd"`

	// UserPromptSubmit
	Prompt string `json:"prompt"`

	// SessionStart
	Source       string `json:"source"`
	Model        string `json:"model"`
	SessionTitle string `json:"session_title"`

	// Stop / SubagentStop
	LastAssistantMessage string           `json:"last_assistant_message"`
	BackgroundTasks      []BackgroundTask `json:"background_tasks"`
	SessionCrons         []SessionCron    `json:"session_crons"`

	// Subagent{Start,Stop}
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`

	// Notification
	Message          string `json:"message"`
	NotificationType string `json:"notification_type"`
}

// BackgroundTask is one entry of the `background_tasks[]` array that Stop and
// SubagentStop carry. It is the closest thing the harness has to a structured
// "what is still running" report, and it arrives free of charge at every turn
// boundary — no polling, no agent cooperation.
type BackgroundTask struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Description string `json:"description"`
	Command     string `json:"command"`
	AgentType   string `json:"agent_type"`
	Server      string `json:"server"`
	Tool        string `json:"tool"`
	Name        string `json:"name"`
}

// SessionCron is one scheduled wakeup: CronCreate, ScheduleWakeup, or /loop.
type SessionCron struct {
	ID        string `json:"id"`
	Schedule  string `json:"schedule"`
	Recurring bool   `json:"recurring"`
	Prompt    string `json:"prompt"`
}

// blockingNotifications are the notification types that mean a human is the
// only thing that can move this session forward.
//
// Not every notification is a block. `agent_completed` and the quota-resume
// family are informational — treating them as blocks would park finished
// sessions in the Blocked column, which is exactly the noise the column
// exists to cut through.
var blockingNotifications = map[string]bool{
	"permission_prompt":  true,
	"idle_prompt":        true,
	"agent_needs_input":  true,
	"elicitation_dialog": true,
	// An elicitation that needs a browser is still a human waiting.
	"elicitation_url_dialog": true,
}

// Handle applies one event to the board.
func Handle(db *store.DB, ev Event) error {
	switch ev.HookEventName {
	case "SessionStart":
		return handleSessionStart(db, ev)
	case "UserPromptSubmit":
		return handleUserPrompt(db, ev)
	case "Notification":
		return handleNotification(db, ev)
	case "Stop":
		return handleStop(db, ev)
	case "SubagentStart":
		return handleSubagentStart(db, ev)
	case "SubagentStop":
		return handleSubagentStop(db, ev)
	case "SessionEnd":
		return db.EndSession(ev.SessionID)
	}
	return nil // an event brd does not model is not an error
}

func handleSessionStart(db *store.DB, ev Event) error {
	repo := repoRoot(ev.Cwd)
	u := store.Upsert{ID: ev.SessionID, Repo: &repo, Cwd: &ev.Cwd}
	if ev.Model != "" {
		u.Model = &ev.Model
	}
	if ev.SessionTitle != "" {
		u.Title = &ev.SessionTitle
	}
	// State is only claimed for a genuinely new conversation. SessionStart
	// also fires for `compact` — and auto-compaction happens *mid-turn* — and
	// for `resume` and `fork`, where the session may already be working. In
	// those cases asserting a state here would move a working session into
	// Done until its next turn boundary. Leaving state alone is always safe:
	// the next UserPromptSubmit or Stop settles it either way.
	if ev.Source == "startup" || ev.Source == "clear" {
		// A conversation with no turn in flight yet. Saying `working` here
		// would light up every window the moment a shell opens one.
		state := store.StateDone
		u.State = &state
	}
	if _, err := db.Apply(u); err != nil {
		return err
	}
	return db.LogEvent(ev.SessionID, "session_start", ev.Source)
}

func handleUserPrompt(db *store.DB, ev Event) error {
	repo := repoRoot(ev.Cwd)
	state := store.StateWorking
	cleared := ""
	u := store.Upsert{
		ID:        ev.SessionID,
		Repo:      &repo,
		Cwd:       &ev.Cwd,
		State:     &state,
		BlockedOn: &cleared,
		BumpTurn:  true,
	}
	// A synthetic prompt still means the session is working, but it must not
	// become what the item is called. UserPromptSubmit fires for prompts the
	// harness generates too — a background task finishing, a cron waking the
	// session — and the live board titled an item "<task-notification>" within
	// minutes of install. The turn counts; the text does not.
	if subject := firstLine(ev.Prompt, 200); !synthetic(subject) {
		u.Subject = &subject
		// The first real prompt names the item; later ones only move the
		// subject, so a long session keeps the title it is remembered by.
		if !db.HasTitle(ev.SessionID) {
			u.Title = &subject
		}
	}
	if _, err := db.Apply(u); err != nil {
		return err
	}
	return db.LogEvent(ev.SessionID, "prompt", firstLine(ev.Prompt, 200))
}

func handleNotification(db *store.DB, ev Event) error {
	if !blockingNotifications[ev.NotificationType] {
		return nil
	}
	state := store.StateWaiting
	blocked := ev.NotificationType
	u := store.Upsert{ID: ev.SessionID, State: &state, BlockedOn: &blocked}
	withIdentity(&u, ev)
	if _, err := db.Apply(u); err != nil {
		return err
	}
	return db.LogEvent(ev.SessionID, "blocked", firstLine(ev.Message, 200))
}

// withIdentity fills in repo and cwd, which every hook event carries as a
// common field.
//
// Only SessionStart and UserPromptSubmit used to set these, so a session first
// seen at a Stop — the common case for a session already running when brd was
// installed, or one resumed with --resume — showed a bare id and no repo on
// the board. Measured on the live board minutes after install: 2 of 3 items
// had empty repo and cwd.
func withIdentity(u *store.Upsert, ev Event) {
	if ev.Cwd == "" {
		return
	}
	repo := repoRoot(ev.Cwd)
	u.Repo = &repo
	u.Cwd = &ev.Cwd
}

// Markers that identify a harness-generated prompt.
//
// UserPromptSubmit fires for prompts Claude Code generates as well as ones a
// person types — a background task finishing, a cron waking the session, a
// slash command expanding. Those advance the turn but must not name the item:
// the live board titled an item "<task-notification>" minutes after install.
//
// Two shapes need distinguishing, because a person can legitimately write a
// prompt that starts with one of these markers — this very session asked "why
// does <task-notification> become the title?", which opens with the tag and is
// an ordinary request.
var (
	// syntheticBareTags arrive as the marker and nothing else on the first
	// line; the payload follows below. A marker with the user's own words
	// after it on the same line is therefore the user's prompt.
	syntheticBareTags = []string{
		"<task-notification>",
		"<system-reminder>",
	}
	// syntheticWrappers enclose their payload on one line, so the closing tag
	// is the tell. A person quoting the tag does not close it.
	syntheticWrappers = []string{
		// system-reminder appears in both shapes: opening a multi-line
		// payload, and closed on one line for a short notice.
		"system-reminder",
		"task-notification",
		"local-command-stdout",
		"local-command-stderr",
		"command-message",
		"command-name",
		"command-args",
	}
	// syntheticSentences are verbatim harness prose with no tag at all.
	syntheticSentences = []string{
		"Caveat: The messages below",
	}
)

// synthetic reports whether a prompt was generated by the harness rather than
// typed by the user.
func synthetic(subject string) bool {
	if subject == "" {
		return true
	}
	for _, tag := range syntheticBareTags {
		// firstLine has already cut the payload, so a generated prompt is
		// reduced to exactly the marker.
		if strings.TrimSpace(strings.TrimPrefix(subject, tag)) == "" &&
			strings.HasPrefix(subject, tag) {
			return true
		}
	}
	for _, name := range syntheticWrappers {
		if strings.HasPrefix(subject, "<"+name+">") &&
			strings.Contains(subject, "</"+name+">") {
			return true
		}
	}
	for _, sentence := range syntheticSentences {
		if strings.HasPrefix(subject, sentence) {
			return true
		}
	}
	return false
}

// handleStop is where the board's most important distinction is made.
//
// A turn ending does not mean the work ended. `background_tasks[]` tells us
// which it was: empty means the session is genuinely idle and waiting for the
// user; non-empty means it is parked until its own shell job, subagent, or
// monitor wakes it back up. Both fire the identical Stop event, so a board
// that ignores the array shows every long-running agent as finished.
func handleStop(db *store.DB, ev Event) error {
	kids := make([]store.Child, 0, len(ev.BackgroundTasks)+len(ev.SessionCrons))
	for _, bt := range ev.BackgroundTasks {
		kids = append(kids, store.Child{
			Kind:   bt.Type,
			Ref:    bt.ID,
			Label:  backgroundLabel(bt),
			State:  bt.Status,
			Detail: firstLine(bt.Description, 160),
		})
	}
	// A cron is not running, but it is a reason this session will come back,
	// so an item holding one is not finished either.
	for _, c := range ev.SessionCrons {
		kids = append(kids, store.Child{
			Kind:   store.KindCron,
			Ref:    c.ID,
			Label:  c.Schedule,
			State:  "scheduled",
			Detail: firstLine(c.Prompt, 160),
		})
	}
	if err := db.ReplaceBackground(ev.SessionID, kids); err != nil {
		return err
	}

	state := store.StateDone
	if len(kids) > 0 {
		state = store.StateBackground
	}
	cleared := ""
	u := store.Upsert{
		ID:          ev.SessionID,
		State:       &state,
		BlockedOn:   &cleared,
		LastMessage: ptr(firstLine(ev.LastAssistantMessage, 400)),
	}
	withIdentity(&u, ev)
	if _, err := db.Apply(u); err != nil {
		return err
	}
	return db.LogEvent(ev.SessionID, "stop", state)
}

func handleSubagentStart(db *store.DB, ev Event) error {
	// A subagent's own hooks carry agent_id; the parent item is still the
	// session, so children hang off session_id either way.
	if u := (store.Upsert{ID: ev.SessionID}); ev.Cwd != "" {
		withIdentity(&u, ev)
		if _, err := db.Apply(u); err != nil {
			return err
		}
	}
	err := db.PutChild(ev.SessionID, store.Child{
		Kind:  store.KindSubagent,
		Ref:   ev.AgentID,
		Label: ev.AgentType,
		State: "running",
	})
	if err != nil {
		return err
	}
	return db.LogEvent(ev.SessionID, "subagent_start", ev.AgentType)
}

func handleSubagentStop(db *store.DB, ev Event) error {
	// Dropped rather than marked done: a finished subagent is not in flight,
	// and the board's job is showing what is. Its final message goes to the
	// audit trail, which is where the history belongs.
	if err := db.DropChild(ev.SessionID, store.KindSubagent, ev.AgentID); err != nil {
		return err
	}
	detail := ev.AgentType
	if msg := firstLine(ev.LastAssistantMessage, 200); msg != "" {
		detail += ": " + msg
	}
	return db.LogEvent(ev.SessionID, "subagent_stop", detail)
}

// backgroundLabel picks the most identifying field for a task type. The
// harness puts the useful name in a different field per type, so a generic
// label would read "shell" for every shell job on the board.
func backgroundLabel(bt BackgroundTask) string {
	switch {
	case bt.AgentType != "":
		return bt.AgentType
	case bt.Name != "":
		return bt.Name
	case bt.Tool != "":
		return bt.Tool
	case bt.Server != "":
		return bt.Server
	case bt.Command != "":
		return firstLine(bt.Command, 80)
	}
	return bt.Type
}

// Read parses one hook payload from r.
func Read(data []byte) (Event, error) {
	var ev Event
	if err := json.Unmarshal(data, &ev); err != nil {
		return ev, err
	}
	return ev, nil
}

// repoRoot resolves cwd to its git root so sessions in different checkouts of
// one repo group together. Falls back to cwd, which keeps non-repo
// directories on the board instead of dropping them into an empty group.
func repoRoot(cwd string) string {
	if cwd == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel")
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return filepath.Clean(cwd)
	}
	if root := strings.TrimSpace(string(out)); root != "" {
		return root
	}
	return filepath.Clean(cwd)
}

// firstLine collapses a payload string to one bounded line. Prompts and
// assistant messages are arbitrarily long and arrive on every turn; storing
// them whole would grow the database without making the board any clearer.
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if len(s) > max {
		// Trim on a rune boundary so a truncated multi-byte character does
		// not land in the database as a replacement glyph.
		for max > 0 && !isRuneStart(s[max]) {
			max--
		}
		s = strings.TrimSpace(s[:max]) + "…"
	}
	return s
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

func ptr[T any](v T) *T { return &v }

// ExitQuiet ends a hook successfully regardless of what went wrong.
//
// A hook that exits non-zero on some events blocks the session outright
// (TaskCompleted, SubagentStop) or surfaces an error to the user. brd is an
// observer; it has no business interrupting the work it observes.
func ExitQuiet(err error) {
	if err != nil {
		// stderr on a non-blocking exit is shown to the user only, never to
		// Claude, so this is diagnosable without polluting the conversation.
		os.Stderr.WriteString("brd: " + err.Error() + "\n")
	}
	os.Exit(0)
}
