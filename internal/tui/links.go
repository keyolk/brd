package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/brd/internal/store"
	"github.com/keyolk/tuikit/keys"
	"github.com/keyolk/tuikit/link"
	"github.com/keyolk/tuikit/toast"
)

// Links (E11). A card is a Claude session, which has no page of its own; what
// it carries URLs for is the pull requests it opened and whatever its last
// message pasted. The PR badge on the card and each PR title in the detail
// pane are OSC 8 hyperlinks, so cmd/ctrl+click opens them even under mouse
// capture. The last message is printed as is: a URL there is clickable as
// plain text where the terminal detects it, and `o` reaches it otherwise.
//
// The ticket key has no URL: brd has no Jira base configured, and guessing one
// would open the wrong site for anyone else's tracker.

// openLinkBinding is `o` on the board. It lives in ctxBoard, which the detail
// stack includes, so it also works with the detail pane open.
func openLinkBinding() keys.Binding {
	b := link.Binding(ctxBoard)
	b.Group, b.Footer = "Act", 4
	return b
}

// cardLinks are the URLs `o` can open for a card, best first: the PR the badge
// names, the card's other PRs, then URLs in its last message.
func (m Model) cardLinks(it store.Item) []string {
	var out []string
	seen := map[string]bool{}
	add := func(u string) {
		if link.Valid(u) && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	prs := m.pullsFor(it)
	if p, ok := badgePull(prs); ok {
		add(p.URL)
	}
	for _, p := range prs {
		add(p.URL)
	}
	for _, u := range link.Find(it.LastMessage) {
		add(u)
	}
	return out
}

// openLink opens the selected card's best URL. With several, it opens the
// first and says how many there were; the detail pane lists them all.
func (m Model) openLink() (tea.Model, tea.Cmd) {
	it, ok := m.selected()
	if !ok {
		return m, nil
	}
	urls := m.cardLinks(it)
	switch len(urls) {
	case 0:
		return m, m.notify("link", toast.Info, "no link on this card")
	case 1:
		return m, link.Open(urls[0])
	}
	note := m.notify("links", toast.Info, fmt.Sprintf("%d links on this card; opening the first", len(urls)))
	return m, tea.Batch(note, link.Open(urls[0]))
}
