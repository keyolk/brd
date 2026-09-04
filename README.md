# brd

A kanban board for the Claude Code sessions on this machine, fed entirely by
hook events.

```
brd  2 waiting on you · 3 live
Blocked 2                    │Working 1                    │Background 2                 │Done 1
▌ PR 리뷰 코멘트를 반영해줘   │  칸반 보드 리서치부터 해보자  │  hybrid 검색 벤치마크         │  audit_log 시크릿 정리
  ghx 12m permission_prompt  │  brd 40s                    │  kmd 8m shell subagent×2    │  delight-ops-k8s 2h0m
  istiod 업그레이드 롤아웃    │                             │  배포 상태 계속 지켜봐줘      │
  ops-k8s 3m agent_needs_input│                            │  tweb 22m cron              │

hjkl move · ↵ detail · r refresh · q quit
```

## Why

With a dozen sessions open, the question that costs the most time is *which
one is waiting on me, and what for?* `twm` answers the first half from tmux
state. brd answers both, and adds the half neither can see: whether a session
that stopped talking is **finished** or **parked on its own background work**.

## The agent is never asked to report

Every existing board for AI agents — [kaban][kaban], [flux][flux],
[kanban-tui][ktui] — has the agent write its own status through an MCP tool.
That makes the board only as honest as the agent's discipline: an agent that
forgets to update leaves a stale card, and an agent that is stuck can hide by
staying quiet. [openkanban][okb] avoids that by polling process state, but
then it can only see *that* something stalled, never *why*.

brd derives every state from hook events the harness pushes on its own. There
is no MCP server, no tool for the agent to call, and nothing to remember. A
session that has never heard of brd still appears on it, correctly.

Seven events carry everything:

| Event | What it establishes |
|---|---|
| `SessionStart` | repo, model, title |
| `UserPromptSubmit` | **Working**, and the prompt names the item |
| `Notification` | **Blocked**, and *what* it is blocked on |
| `Stop` | **Done**, or **Background** — see below |
| `SubagentStart` / `SubagentStop` | children appear and resolve |
| `SessionEnd` | ended |

### The distinction that matters

`Stop` fires identically whether a session finished or is merely parked
waiting for its own shell job to wake it up. The only thing separating them is
whether `background_tasks[]` came back empty:

```json
"background_tasks": [
  { "id": "t9", "type": "shell", "status": "running", "command": "go test ./... -race" },
  { "id": "a1", "type": "subagent", "status": "running", "agent_type": "Explore" }
]
```

A board that ignores that array shows every long-running agent as finished.
That array is also a *complete snapshot*, so brd replaces its background rows
wholesale rather than merging — a task missing from a later `Stop` has ended,
and merging would pin finished jobs to the board forever.

Subagent rows are the exception: `SubagentStart`/`Stop` own them, because they
carry explicit edges and a subagent mid-flight at a turn boundary would
otherwise be dropped and re-added, resetting its age.

### Not every notification is a block

Only the types a human can clear count: `permission_prompt`, `idle_prompt`,
`agent_needs_input`, `elicitation_dialog`, `elicitation_url_dialog`. Treating
`agent_completed` or the quota-resume family as blocks would park finished
sessions in the one column that has to stay short to mean anything.

## Install

```sh
make install                    # -> ~/.local/bin/brd
brd install                     # prints the settings.json block to merge
```

The hook block, for `~/.claude/settings.json`:

```json
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
```

`brd hook` exits 0 on every failure it can reach. A hook that exits non-zero
blocks the session from stopping (`Stop`, `SubagentStop`) or surfaces an error
in the transcript — brd is an observer and must not interrupt what it watches.

## Usage

```sh
brd          # the board
brd ls       # the board as text, for a scratch pane or a status line
```

| Key | |
|---|---|
| `hjkl` / arrows | move between columns and cards |
| `↵` | detail: children, cwd, model, turns, last message |
| `b` | jump to the first blocked item |
| `r` | refresh now |
| `q` | quit (closes the detail pane first) |

Keys appear in the hint bar only when they would do something. A bar wider
than the terminal truncates from the right, so a bar that lists everything
hides the keys a lost user needs most.

## Storage

One SQLite file, `$BRD_DB` or `~/.local/share/brd/board.db`, in WAL mode with
a busy timeout — hooks from unrelated sessions write concurrently, and a lost
state transition is the one failure that makes the whole board untrustworthy.

The board shows the last 24 hours. Older sessions are history, and history
belongs in [`ccx`](https://github.com/sendbird/ccx).

## What brd is not

- **Not a task tracker.** `TaskCreate`/`TaskUpdate` and their
  `blocks`/`blockedBy` graph are off by default on Opus 4.8, Sonnet 5, Fable 5
  and later ([tools-reference][ttools]), so on this machine that surface is
  empty. `~/.claude/tasks/<session>/` is a possible import source, never the
  store.
- **Not a session browser.** `ccx` reads transcripts, agent hierarchies and
  stats. brd shows only what is in flight right now.
- **Not an orchestrator.** It starts nothing and stops nothing.

## Related

`ghx` (PRs) · `tmc` (tmux workspace) · `ccx` (sessions) · brd (work in flight)

[kaban]: https://kaban-board.github.io/kaban/
[flux]: https://paddo.dev/blog/flux-kanban-for-ai-agents/
[ktui]: https://github.com/Zaloog/kanban-tui
[okb]: https://github.com/TechDufus/openkanban
[ttools]: https://code.claude.com/docs/en/tools-reference#task-tool-availability
