package tui

import (
	"strings"
	"time"

	"github.com/keyolk/brd/internal/store"
)

// How an item is named and stamped on the board. Split out of view.go because
// `brd ls` needs the same answers the renderer does, and because both were
// rewritten against measured failures rather than designed up front.

// Label is how an item is named on the board.
//
// The order matters and was set by what the live board actually produced. The
// title Claude Code writes for itself (`ai-title`) is first, because it
// describes what the session is *about*: every one of 20 sampled sessions had
// one. A prompt is a poor name by comparison — of 14 items named from prompts,
// 6 were unreadable — so a prompt is used only after being checked, and a bare
// session uuid is the last resort rather than a common outcome.
//
// A ticket, when there is one, prefixes the name: several sessions on one
// ticket are the case grouping exists for, and the prefix is what makes them
// recognizable as the same piece of work before you group them.
func Label(it store.Item) string {
	name := it.Title
	if name == "" {
		name = readableSubject(it.Subject)
	}
	if name == "" {
		return shortID(it.ID)
	}
	if it.Ticket != "" && !strings.Contains(name, it.Ticket) {
		return it.Ticket + " " + name
	}
	return name
}

// readableSubject returns the subject only if it would tell a human anything.
//
// Measured on a live board of 14 items: a bare uuid (a cross-session wake-up
// naming its peer), a line of pasted Slack permalinks, and a bare "/skill"
// invocation all became titles. None of them says what the work is, and each
// crowds out the repo and state that do.
func readableSubject(subject string) string {
	s := strings.TrimSpace(subject)
	switch {
	case s == "":
		return ""
	case looksLikeUUID(s):
		return ""
	case strings.HasPrefix(s, "http://"), strings.HasPrefix(s, "https://"):
		return ""
	case strings.HasPrefix(s, "/"):
		// A slash command carries no description of its own.
		return ""
	case strings.HasPrefix(s, "<"):
		// Any remaining harness envelope the hook filter did not catch.
		return ""
	}
	return s
}

// looksLikeUUID matches the 8-4-4-4-12 form, which is how a session id reads.
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// shortID is the first segment of a session uuid — enough to tell two
// nameless items apart without spending 36 columns to do it.
func shortID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// Stamp is the wall-clock time of an item's last activity.
//
// An age alone ("2h13m") answers how stale something is but not when it
// happened, and the two are different questions: "did this stall before or
// after I went to lunch" needs the clock. Both are shown — the age scans
// faster, the stamp is what you correlate against everything else.
func Stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	if time.Since(t) > 18*time.Hour {
		// Past that, the time of day alone is ambiguous about which day.
		return t.Format("Jan 2 15:04")
	}
	return t.Format("15:04")
}
