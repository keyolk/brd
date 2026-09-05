package hook

import (
	"path/filepath"
	"strings"
)

// Labels for the things a session has in flight. These exist because the raw
// fields the harness reports are not names: the live board showed three rows
// of one session all reading the same truncated scratch path, which is three
// different jobs rendered indistinguishably.

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
	case bt.Description != "":
		// A description is a human summary; a command is not. Preferring it
		// avoids the failure below where the command line is all path.
		return firstLine(bt.Description, 60)
	case bt.Command != "":
		return commandLabel(bt.Command)
	}
	return bt.Type
}

// commandLabel reduces a shell command to the part that says what it does.
//
// The live board showed three rows of a session reading
// "/private/tmp/claude-502/-Users-gavin-jeong-src-sendbird-mitm-proxy/260a…"
// — three different jobs, all truncated inside the same scratch path, none
// distinguishable. Leading assignments and long paths are the noise; the
// program being run is the signal.
func commandLabel(command string) string {
	line := firstLine(command, 400)
	fields := strings.Fields(line)
	// Skip VAR=value prefixes, which is how these commands usually open.
	for len(fields) > 0 && strings.Contains(fields[0], "=") &&
		!strings.HasPrefix(fields[0], "/") {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return firstLine(command, 60)
	}
	// A path as the program name says nothing that its basename does not.
	// Length is the wrong test — "/usr/bin/env" is short and still reads
	// better as "env" — so any absolute path is reduced.
	head := fields[0]
	if strings.HasPrefix(head, "/") {
		head = filepath.Base(head)
	}
	out := head
	// One argument of context, when it is not itself a path.
	if len(fields) > 1 && !strings.HasPrefix(fields[1], "/") {
		out += " " + fields[1]
	}
	return firstLine(out, 60)
}
