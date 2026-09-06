package hook

import (
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/keyolk/brd/internal/store"
)

// open gives each test its own board. The database path comes from
// t.TempDir() rather than BRD_DB in the environment, so a real board on this
// machine is never touched and the tests stay hermetic on a dev box.
func open(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func only(t *testing.T, db *store.DB) store.Item {
	t.Helper()
	items, err := db.Items(time.Hour)
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	return items[0]
}

func apply(t *testing.T, db *store.DB, evs ...Event) {
	t.Helper()
	for i, ev := range evs {
		if ev.SessionID == "" {
			ev.SessionID = "s1"
		}
		if err := Handle(db, ev); err != nil {
			t.Fatalf("event %d (%s): %v", i, ev.HookEventName, err)
		}
	}
}

// The distinction this whole design rests on: Stop fires identically whether
// a session is finished or merely parked, and only background_tasks[] tells
// them apart. Getting this wrong shows every long-running agent as done.
func TestStopWithBackgroundTasksIsNotDone(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{HookEventName: "UserPromptSubmit", Prompt: "build the thing"},
		Event{HookEventName: "Stop", BackgroundTasks: []BackgroundTask{
			{ID: "t1", Type: "shell", Status: "running", Command: "make test"},
		}},
	)
	if got := only(t, db); got.State != store.StateBackground {
		t.Errorf("state = %q, want %q", got.State, store.StateBackground)
	}
}

func TestStopWithNoBackgroundTasksIsDone(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{HookEventName: "UserPromptSubmit", Prompt: "build the thing"},
		Event{HookEventName: "Stop", LastAssistantMessage: "done"},
	)
	got := only(t, db)
	if got.State != store.StateDone {
		t.Errorf("state = %q, want %q", got.State, store.StateDone)
	}
	if len(got.Children) != 0 {
		t.Errorf("children = %d, want 0", len(got.Children))
	}
}

// A cron is not running, but it is a reason the session will wake up again,
// so an item holding one has not finished either.
func TestStopWithOnlyCronIsBackground(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{HookEventName: "UserPromptSubmit", Prompt: "watch the deploy"},
		Event{HookEventName: "Stop", SessionCrons: []SessionCron{
			{ID: "c1", Schedule: "*/5 * * * *", Recurring: true, Prompt: "check"},
		}},
	)
	got := only(t, db)
	if got.State != store.StateBackground {
		t.Errorf("state = %q, want %q", got.State, store.StateBackground)
	}
	if len(got.Children) != 1 || got.Children[0].Kind != store.KindCron {
		t.Errorf("children = %+v, want one cron", got.Children)
	}
}

// background_tasks[] is a full snapshot, so a task absent from a later Stop
// has finished. Merging instead of replacing pins finished jobs to the board
// forever, which is the failure that makes a board untrustworthy.
func TestBackgroundTasksAreReplacedNotMerged(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{HookEventName: "UserPromptSubmit", Prompt: "go"},
		Event{HookEventName: "Stop", BackgroundTasks: []BackgroundTask{
			{ID: "t1", Type: "shell", Status: "running", Command: "make test"},
			{ID: "t2", Type: "monitor", Status: "running", Server: "portal"},
		}},
		Event{HookEventName: "Stop", BackgroundTasks: []BackgroundTask{
			{ID: "t2", Type: "monitor", Status: "running", Server: "portal"},
		}},
	)
	got := only(t, db)
	if len(got.Children) != 1 {
		t.Fatalf("children = %+v, want only t2", got.Children)
	}
	if got.Children[0].Ref != "t2" {
		t.Errorf("surviving child = %q, want t2", got.Children[0].Ref)
	}
}

// Subagent rows are owned by SubagentStart/Stop. A Stop landing between them
// must not clear a subagent that is still running.
func TestStopDoesNotClearRunningSubagent(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{HookEventName: "UserPromptSubmit", Prompt: "investigate"},
		Event{HookEventName: "SubagentStart", AgentID: "a1", AgentType: "Explore"},
		Event{HookEventName: "Stop", BackgroundTasks: []BackgroundTask{
			{ID: "a1", Type: "subagent", Status: "running", AgentType: "Explore"},
		}},
	)
	got := only(t, db)
	if len(got.Children) != 1 {
		t.Fatalf("children = %+v, want the subagent to survive", got.Children)
	}
	if c := got.Children[0]; c.Kind != store.KindSubagent || c.Ref != "a1" {
		t.Errorf("child = %+v, want subagent a1", c)
	}
}

func TestSubagentStopRemovesChild(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{HookEventName: "UserPromptSubmit", Prompt: "investigate"},
		Event{HookEventName: "SubagentStart", AgentID: "a1", AgentType: "Explore"},
		Event{HookEventName: "SubagentStop", AgentID: "a1", AgentType: "Explore",
			LastAssistantMessage: "found it"},
	)
	if got := only(t, db); len(got.Children) != 0 {
		t.Errorf("children = %+v, want none", got.Children)
	}
}

// Only notifications a human can clear are blocks. Treating the rest as
// blocks parks finished sessions in the column that exists to be short.
func TestOnlyBlockingNotificationsBlock(t *testing.T) {
	blocking := []string{
		"permission_prompt", "agent_needs_input",
		"elicitation_dialog", "elicitation_url_dialog",
	}
	for _, nt := range blocking {
		t.Run("blocks/"+nt, func(t *testing.T) {
			db := open(t)
			apply(t, db,
				Event{HookEventName: "UserPromptSubmit", Prompt: "go"},
				Event{HookEventName: "Notification", NotificationType: nt},
			)
			got := only(t, db)
			if got.State != store.StateWaiting {
				t.Errorf("state = %q, want %q", got.State, store.StateWaiting)
			}
			if got.BlockedOn != nt {
				t.Errorf("blocked_on = %q, want %q", got.BlockedOn, nt)
			}
			if got.Column() != "Blocked" {
				t.Errorf("column = %q, want Blocked", got.Column())
			}
		})
	}

	informational := []string{
		"agent_completed", "auth_success", "quota_auto_resume_fired",
		"quota_auto_resume_stale", "elicitation_complete",
	}
	for _, nt := range informational {
		t.Run("ignores/"+nt, func(t *testing.T) {
			db := open(t)
			apply(t, db,
				Event{HookEventName: "UserPromptSubmit", Prompt: "go"},
				Event{HookEventName: "Notification", NotificationType: nt},
			)
			if got := only(t, db); got.State != store.StateWorking {
				t.Errorf("state = %q, want %q (unchanged)", got.State, store.StateWorking)
			}
		})
	}
}

// A prompt after a block clears it. Without this, an item answered by the
// user stays in Blocked and the column stops meaning anything.
func TestPromptClearsBlock(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{HookEventName: "UserPromptSubmit", Prompt: "go"},
		Event{HookEventName: "Notification", NotificationType: "permission_prompt"},
		Event{HookEventName: "UserPromptSubmit", Prompt: "yes, proceed"},
	)
	got := only(t, db)
	if got.State != store.StateWorking {
		t.Errorf("state = %q, want %q", got.State, store.StateWorking)
	}
	if got.BlockedOn != "" {
		t.Errorf("blocked_on = %q, want empty", got.BlockedOn)
	}
}

// The first prompt names the item; later prompts must not rename it, or a
// long session loses the label it is remembered by halfway through.
func TestTitleStaysFromFirstPrompt(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{HookEventName: "UserPromptSubmit", Prompt: "add the kanban board"},
		Event{HookEventName: "UserPromptSubmit", Prompt: "push"},
	)
	got := only(t, db)
	if got.Title != "add the kanban board" {
		t.Errorf("title = %q, want the first prompt", got.Title)
	}
	if got.Subject != "push" {
		t.Errorf("subject = %q, want the latest prompt", got.Subject)
	}
	if got.Turns != 2 {
		t.Errorf("turns = %d, want 2", got.Turns)
	}
}

// A session that just started has no turn in flight. Marking it working
// would light up every window the moment a shell opens one.
func TestSessionStartIsNotWorking(t *testing.T) {
	db := open(t)
	apply(t, db, Event{HookEventName: "SessionStart", Source: "startup",
		Model: "claude-opus-5"})
	got := only(t, db)
	if got.State == store.StateWorking {
		t.Errorf("a fresh session should not be %q", store.StateWorking)
	}
	if got.Model != "claude-opus-5" {
		t.Errorf("model = %q, want claude-opus-5", got.Model)
	}
}

// Nothing a session had in flight outlives the process, so an ended session
// must not leave children on the board claiming to still run.
func TestSessionEndClearsChildren(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{HookEventName: "UserPromptSubmit", Prompt: "go"},
		Event{HookEventName: "SubagentStart", AgentID: "a1", AgentType: "Explore"},
		Event{HookEventName: "Stop", BackgroundTasks: []BackgroundTask{
			{ID: "t1", Type: "shell", Status: "running", Command: "sleep 999"},
		}},
		Event{HookEventName: "SessionEnd"},
	)
	got := only(t, db)
	if got.State != store.StateEnded {
		t.Errorf("state = %q, want %q", got.State, store.StateEnded)
	}
	if len(got.Children) != 0 {
		t.Errorf("children = %+v, want none", got.Children)
	}
}

// state_since must survive a state being re-asserted, or "waiting 40m"
// resets to zero on every turn that repeats the same state.
func TestRepeatedStateKeepsItsAge(t *testing.T) {
	db := open(t)
	apply(t, db, Event{HookEventName: "UserPromptSubmit", Prompt: "go"})
	first := only(t, db).StateSince

	apply(t, db,
		Event{HookEventName: "Notification", NotificationType: "permission_prompt"},
		Event{HookEventName: "Notification", NotificationType: "permission_prompt"},
	)
	blockedSince := only(t, db).StateSince
	if !blockedSince.After(first) && !blockedSince.Equal(first) {
		t.Fatalf("blocked_since %v predates working_since %v", blockedSince, first)
	}

	// A third identical notification must not move the stamp again.
	apply(t, db, Event{HookEventName: "Notification", NotificationType: "permission_prompt"})
	if got := only(t, db).StateSince; !got.Equal(blockedSince) {
		t.Errorf("state_since moved on a repeated state: %v -> %v", blockedSince, got)
	}
}

// An unmodeled event must be a no-op, not an error: brd is an observer and a
// hook returning failure can block the session it watches.
func TestUnknownEventIsIgnored(t *testing.T) {
	db := open(t)
	if err := Handle(db, Event{HookEventName: "PreToolUse", SessionID: "s1"}); err != nil {
		t.Errorf("unmodeled event returned %v, want nil", err)
	}
	items, err := db.Items(time.Hour)
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("unmodeled event created %d items, want 0", len(items))
	}
}

// Two sessions in flight must not collide — the common case on this machine.
func TestSessionsAreIndependent(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{SessionID: "s1", HookEventName: "UserPromptSubmit", Prompt: "first"},
		Event{SessionID: "s2", HookEventName: "UserPromptSubmit", Prompt: "second"},
		Event{SessionID: "s1", HookEventName: "Notification",
			NotificationType: "permission_prompt"},
	)
	items, err := db.Items(time.Hour)
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	byID := map[string]store.Item{}
	for _, it := range items {
		byID[it.ID] = it
	}
	if got := byID["s1"].State; got != store.StateWaiting {
		t.Errorf("s1 state = %q, want %q", got, store.StateWaiting)
	}
	if got := byID["s2"].State; got != store.StateWorking {
		t.Errorf("s2 state = %q, want %q", got, store.StateWorking)
	}
}

func TestBackgroundLabelPicksTheIdentifyingField(t *testing.T) {
	cases := []struct {
		name string
		task BackgroundTask
		want string
	}{
		{"subagent", BackgroundTask{Type: "subagent", AgentType: "Explore"}, "Explore"},
		{"workflow", BackgroundTask{Type: "workflow", Name: "review"}, "review"},
		{"monitor", BackgroundTask{Type: "monitor", Server: "portal", Tool: "watch"}, "watch"},
		{"mcp server only", BackgroundTask{Type: "MCP task", Server: "portal"}, "portal"},
		{"shell", BackgroundTask{Type: "shell", Command: "make test"}, "make test"},
		{"bare", BackgroundTask{Type: "shell"}, "shell"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := backgroundLabel(tc.task); got != tc.want {
				t.Errorf("backgroundLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

// Prompts arrive with arbitrary length and newlines on every turn, and are
// often Korean here — truncation must not split a multi-byte rune.
func TestFirstLineIsBoundedAndRuneSafe(t *testing.T) {
	cases := []struct {
		name, in string
		max      int
		want     string
	}{
		{"empty", "   ", 10, ""},
		{"single line", "hello", 10, "hello"},
		{"multiline takes the first", "first\nsecond", 40, "first"},
		{"crlf", "first\r\nsecond", 40, "first"},
		{"trims", "  spaced  ", 40, "spaced"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstLine(tc.in, tc.max); got != tc.want {
				t.Errorf("firstLine = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("korean stays valid utf8", func(t *testing.T) {
		got := firstLine("칸반 보드를 만들어 보자 그리고 더 길게 이어서 씁니다", 20)
		if !utf8.ValidString(got) {
			t.Errorf("firstLine produced invalid UTF-8: %q", got)
		}
		if len(got) > 20+len("…") {
			t.Errorf("firstLine = %q, longer than the cap", got)
		}
	})
}

// Auto-compaction fires SessionStart *mid-turn* with source "compact", and
// resume/fork can land on a session that is already working. Asserting a
// state there would move a working session into Done until its next turn
// boundary — the board would report the opposite of the truth.
func TestSessionStartMidTurnKeepsWorkingState(t *testing.T) {
	for _, source := range []string{"compact", "resume", "fork"} {
		t.Run(source, func(t *testing.T) {
			db := open(t)
			apply(t, db,
				Event{HookEventName: "UserPromptSubmit", Prompt: "long investigation"},
				Event{HookEventName: "SessionStart", Source: source, Model: "claude-opus-5"},
			)
			got := only(t, db)
			if got.State != store.StateWorking {
				t.Errorf("source %q changed state to %q, want %q",
					source, got.State, store.StateWorking)
			}
			// It must still pick up the identity fields it carries.
			if got.Model != "claude-opus-5" {
				t.Errorf("model = %q, want claude-opus-5", got.Model)
			}
		})
	}
}

// A blocked session must not be un-blocked by a compaction either.
func TestSessionStartMidTurnKeepsBlockedState(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{HookEventName: "UserPromptSubmit", Prompt: "go"},
		Event{HookEventName: "Notification", NotificationType: "permission_prompt"},
		Event{HookEventName: "SessionStart", Source: "compact"},
	)
	got := only(t, db)
	if got.State != store.StateWaiting {
		t.Errorf("state = %q, want %q", got.State, store.StateWaiting)
	}
	if got.BlockedOn != "permission_prompt" {
		t.Errorf("blocked_on = %q, want permission_prompt", got.BlockedOn)
	}
}

// `clear` genuinely resets the conversation, so it does settle the state.
func TestSessionStartClearResetsState(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{HookEventName: "UserPromptSubmit", Prompt: "go"},
		Event{HookEventName: "SessionStart", Source: "clear"},
	)
	if got := only(t, db); got.State != store.StateDone {
		t.Errorf("state = %q, want %q after /clear", got.State, store.StateDone)
	}
}

// Every hook event carries cwd, and a session first seen at a Stop — one
// already running when brd was installed, or resumed with --resume — must
// still get a repo. Measured on the live board: 2 of 3 items had none.
func TestIdentityComesFromAnyEvent(t *testing.T) {
	events := []Event{
		{HookEventName: "Stop", Cwd: "/tmp", BackgroundTasks: nil},
		{HookEventName: "Notification", Cwd: "/tmp", NotificationType: "idle_prompt"},
		{HookEventName: "SubagentStart", Cwd: "/tmp", AgentID: "a1", AgentType: "Explore"},
	}
	for _, ev := range events {
		t.Run(ev.HookEventName, func(t *testing.T) {
			db := open(t)
			apply(t, db, ev)
			got := only(t, db)
			if got.Cwd == "" {
				t.Errorf("%s left cwd empty", ev.HookEventName)
			}
			if got.Repo == "" {
				t.Errorf("%s left repo empty", ev.HookEventName)
			}
		})
	}
}

// An event without cwd must not blank out identity a previous event set.
func TestIdentityIsNotErasedByAnEventWithoutCwd(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{HookEventName: "UserPromptSubmit", Cwd: "/tmp", Prompt: "go"},
		Event{HookEventName: "Notification", NotificationType: "idle_prompt"}, // no cwd
	)
	if got := only(t, db); got.Cwd == "" {
		t.Error("a cwd-less event erased the item's cwd")
	}
}

// UserPromptSubmit also fires for prompts the harness generates — a background
// task finishing, a cron firing. Those advance the turn but must not name the
// item: the live board titled one "<task-notification>" minutes after install.
func TestSyntheticPromptsDoNotNameTheItem(t *testing.T) {
	synthetic := []string{
		"<task-notification>",
		"<task-notification>\n<task-id>abc</task-id>",
		"<system-reminder>something happened</system-reminder>",
		"<local-command-stdout>output</local-command-stdout>",
		"<command-message>skill running</command-message>",
		"<command-name>/loop</command-name>",
		"Caveat: The messages below were generated while running",
	}
	for _, prompt := range synthetic {
		t.Run(firstLine(prompt, 24), func(t *testing.T) {
			db := open(t)
			apply(t, db,
				Event{HookEventName: "UserPromptSubmit", Cwd: "/tmp",
					Prompt: "실제 사용자 요청"},
				Event{HookEventName: "UserPromptSubmit", Cwd: "/tmp", Prompt: prompt},
			)
			got := only(t, db)
			if got.Title != "실제 사용자 요청" {
				t.Errorf("title = %q, want the real prompt", got.Title)
			}
			if got.Subject != "실제 사용자 요청" {
				t.Errorf("subject = %q, want the real prompt", got.Subject)
			}
			// It is still a turn, and the session is still working.
			if got.State != store.StateWorking {
				t.Errorf("state = %q, want %q", got.State, store.StateWorking)
			}
			if got.Turns != 2 {
				t.Errorf("turns = %d, want 2 — a synthetic prompt is still a turn", got.Turns)
			}
		})
	}
}

// A session whose *first* prompt is synthetic has no name yet. It must show
// up on the board anyway rather than being dropped.
func TestSyntheticFirstPromptStillCreatesTheItem(t *testing.T) {
	db := open(t)
	apply(t, db, Event{HookEventName: "UserPromptSubmit", Cwd: "/tmp",
		Prompt: "<task-notification>"})
	got := only(t, db)
	if got.State != store.StateWorking {
		t.Errorf("state = %q, want %q", got.State, store.StateWorking)
	}
	if got.Title != "" {
		t.Errorf("title = %q, want empty until a real prompt arrives", got.Title)
	}
}

// A real prompt that merely mentions one of these markers is not synthetic —
// only a prompt that opens with one is.
func TestRealPromptsAreNotMistakenForSynthetic(t *testing.T) {
	real := []string{
		"<task-notification> 은 왜 제목이 되는거야",
		"system-reminder 태그를 어떻게 걸러?",
		"Caveat emptor",
	}
	for _, prompt := range real {
		t.Run(firstLine(prompt, 24), func(t *testing.T) {
			db := open(t)
			apply(t, db, Event{HookEventName: "UserPromptSubmit", Cwd: "/tmp", Prompt: prompt})
			if got := only(t, db); got.Title != prompt {
				t.Errorf("title = %q, want %q", got.Title, prompt)
			}
		})
	}
}

// idle_prompt was originally classified as blocking, and it was the single
// worst call in the whole design: it fires when a session has been sitting at
// a prompt, which is the same state Stop already reports. Treating it as a
// block put every finished session in the column that exists to be short —
// measured on the live board, 7 of 7 "Blocked" items were this.
func TestIdlePromptIsIdleNotBlocked(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{HookEventName: "UserPromptSubmit", Cwd: "/tmp", Prompt: "go"},
		Event{HookEventName: "Notification", NotificationType: "idle_prompt"},
	)
	got := only(t, db)
	if got.State != store.StateDone {
		t.Errorf("state = %q, want %q", got.State, store.StateDone)
	}
	if got.BlockedOn != "" {
		t.Errorf("blocked_on = %q, want empty", got.BlockedOn)
	}
	if got.Column() != "Idle" {
		t.Errorf("column = %q, want Idle", got.Column())
	}
}

// Reaching an idle prompt means the turn finished, so whatever the session was
// blocked on is resolved. Without this the board keeps showing a permission
// prompt the user already answered.
func TestIdlePromptClearsAnEarlierBlock(t *testing.T) {
	db := open(t)
	apply(t, db,
		Event{HookEventName: "UserPromptSubmit", Cwd: "/tmp", Prompt: "go"},
		Event{HookEventName: "Notification", NotificationType: "permission_prompt"},
		Event{HookEventName: "Notification", NotificationType: "idle_prompt"},
	)
	got := only(t, db)
	if got.BlockedOn != "" {
		t.Errorf("blocked_on = %q, want the stale block cleared", got.BlockedOn)
	}
	if got.State != store.StateDone {
		t.Errorf("state = %q, want %q", got.State, store.StateDone)
	}
}
