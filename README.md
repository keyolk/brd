# brd

A kanban board for the Claude Code sessions on this machine, fed entirely by
hook events.

```
brd  2 waiting on you · 1 working · 6 live
Blocked 2                           │Working 1                           │Background 2                        │Idle 2
▌●apply the review comments         │ ●research kanban boards            │ ●benchmark hybrid search           │ ●clean up stale secrets
  ghx 23:13 · 12m needs permission  │  brd 23:24 · 40s                   │  kmd 23:17 · 8m shell subagent     │  dots 21:25 · 2h0m
  #90 checks failing                │                                    │ ●CPLAT-1204 watch the deploy       │  #71 merged
 ●CPLAT-1204 plan the addon rollout │                                    │  okx 23:03 · 22m cron              │ ·sgl vs vllm comparison
  tweb 23:22 · 3m agent needs input │                                    │  #88 changes requested             │  okx Sep 5 21:25 · 1d
  #88 changes requested             │                                    │                                    │

hjkl move · ↵ detail · J jump · R resume · t group · r refresh · q quit
```

## Why

With a dozen sessions open, the question that costs the most time is *which
one is waiting on me, and what for?* A tmux dashboard can answer the first
half. brd answers both, and adds the half neither can see: whether a session
that stopped talking is **finished** or **parked on its own background work**.

Then it lets you act on the answer — `J` jumps to the session's tmux pane, `R`
resumes an ended one — and follows the work past the session: several sessions
on one JIRA ticket group together (`t`), and the pull requests they opened are
tracked alongside them.

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
| `SessionStart` | repo, model |
| `UserPromptSubmit` | **Working** |
| `Notification` | **Blocked**, and *what* it is blocked on |
| `Stop` | **Idle**, or **Background** — see below |
| `SubagentStart` / `SubagentStop` | children appear and resolve |
| `SessionEnd` | ended |

Every event also carries `cwd` and `transcript_path`, which is where the name,
branch and ticket come from — see below.

### The session already knows what it is about

The first attempt named each item after its first prompt. On a live board of 14
items that produced 6 unreadable names: bare UUIDs (a cross-session wake-up
naming its peer), a line of pasted Slack permalinks, a bare `/sb:pr-followup`,
and two blanks.

Claude Code writes a better name itself. Its transcript carries an `ai-title`
record — what the session is *about*, refreshed as the work moves — and every
one of 20 sampled sessions had one. It also carries `pr-link`, so a PR the
session opened needs no discovery (12 of those 20 had one), and `gitBranch`,
which is where the JIRA ticket lives.

Reading it is cheap enough for a hook: the records sit near the end of the file
— never more than 27 lines from it across 12 sampled transcripts — so brd reads
the last 256 KiB, a few milliseconds even on a 3 MB transcript.

### Grouping: ticket first, then repository

The local convention puts the ticket in the branch name (`CPLAT-11964/desc`,
`CPLAT-11993-gestalt-image-tag`), and a PR guard enforces it, so the branch is
the one place it is guaranteed — and it is there before any PR exists, which is
what makes it a better grouping key than the PR.

Grouping by ticket **alone** was almost inert, though. On a live board of 19
items only 2 carried a ticket, and they were different tickets, so the grouped
view was identical to the ungrouped one. The reasons split three ways: 7 items
had no branch at all (detached checkouts), 7 were on `main` or `master`, and
two real tickets were missed by a case-sensitive pattern — `cplat-11794-…` is
the same ticket as `CPLAT-11794/…`.

So the key falls back to the repository, which nearly every item has. Measured
on the same board that grouped 3 sessions in one column and 4 in another. A
ticket still outranks a repository, because a named piece of work is more
specific than the directory it happens in.

Sharing is judged per column, because grouping is per column: two sessions in
one repository sitting in different columns are not brought together by it, so
the `t` key is not offered for them.

### The distinction that matters

`Stop` fires identically whether a session is idle or merely parked
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

Only the types a human can clear count: `permission_prompt`,
`agent_needs_input`, `elicitation_dialog`, `elicitation_url_dialog`.

`idle_prompt` is deliberately not among them, and classifying it as one was the
worst call in the original design. It fires when a session has been sitting at
a prompt — the same state `Stop` already reports — so treating it as a block
filled the column that exists to be short: on the live board, **7 of 7
"Blocked" items were this**, and none of them were blocked on anything.

## Hooks cannot tell you whether a session still exists

A hook reports a transition and then stops talking. `Stop` says the *turn*
ended, not the session, and a session killed with SIGKILL or a closed terminal
never fires `SessionEnd` at all — so the last hook a session fired stayed on
the board forever whether or not the session outlived it.

Measured against tmux, that was most of the board: of **26 live sessions, brd
disagreed about 16** — ten it showed as finished that were sitting at a prompt,
five it had never heard of, and one it showed as waiting that had ended.

tmux has the answer, because the `cc_state` hook publishes it on the pane
itself and refreshes it every turn. Reading every pane costs 7ms, so brd asks
on every refresh:

| | is the authority on |
|---|---|
| hooks | *why* an item is where it is — which permission, what parked it |
| tmux | *whether the session still exists*, and whether it is mid-turn |

Where they disagree about existence, tmux wins: a hook can only report the
past. A live session brd has never recorded is added from tmux and named from
its own transcript, which is the normal case right after installing the hooks —
8 of those 26 had fired none. A session tmux does not hold leaves the board
entirely.

A `●` marks a live session and a dim `·` a dead one. Without it a board of
mostly-running sessions read as a list of finished work, which is what prompted
all of the above.

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
| `↵` | detail: PRs, children, branch, ticket, last message |
| `b` | jump to the first blocked item |
| `J` | switch to the session's tmux pane |
| `R` | resume the session in this terminal |
| `t` | group by JIRA ticket |
| `r` | refresh now |
| `q` | quit (closes the detail pane first) |

`J` needs no bookkeeping of brd's own: a pane already advertises which Claude
session it holds, in the `@cc_pane_session` option. When there is no live pane
— the session ended — the board says so and points at `R`, which runs
`claude --resume <id>` in the session's own directory and hands the terminal
back when it exits.

Keys appear in the hint bar only when they would do something. A bar wider
than the terminal truncates from the right, so a bar that lists everything
hides the keys a lost user needs most.

## Pull requests are the one thing hooks cannot tell you

The gh CLI takes about a second per call — far too slow to run at every turn
boundary in every session — and a PR changes when no session is running at all,
because review happens on someone else's clock. So the TUI polls once a minute
while it is on screen, and a board nobody is looking at costs nothing.

A card shows the PR that most needs you, not the newest: a failing check on an
older PR outranks a green one opened since. Merged PRs surface only when
nothing is open, because "it landed" is the question you have right after a
session goes quiet.

## Storage

One SQLite file, `$BRD_DB` or `~/.local/share/brd/board.db`, in WAL mode with
a busy timeout — hooks from unrelated sessions write concurrently, and a lost
state transition is the one failure that makes the whole board untrustworthy.

The board shows the last 24 hours. Older sessions are history, and history
belongs in a session browser.

## What brd is not

- **Not a task tracker.** `TaskCreate`/`TaskUpdate` and their
  `blocks`/`blockedBy` graph are off by default on Opus 4.8, Sonnet 5, Fable 5
  and later ([tools-reference][ttools]), so on this machine that surface is
  empty. `~/.claude/tasks/<session>/` is a possible import source, never the
  store.
- **Not a session browser.** Reading transcripts, agent hierarchies and stats
  is a different tool's job. brd shows only what is in flight right now.
- **Not an orchestrator.** It starts nothing and stops nothing.

## Related

brd is one axis — work in flight. Sibling tools cover the others:
[`ghx`](https://github.com/keyolk/ghx) for pull requests,
[`tmc`](https://github.com/keyolk/tmc) for the tmux workspace.

[kaban]: https://kaban-board.github.io/kaban/
[flux]: https://paddo.dev/blog/flux-kanban-for-ai-agents/
[ktui]: https://github.com/Zaloog/kanban-tui
[okb]: https://github.com/TechDufus/openkanban
[ttools]: https://code.claude.com/docs/en/tools-reference#task-tool-availability
