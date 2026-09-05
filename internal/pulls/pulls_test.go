package pulls

import "testing"

// The local directory name is not the GitHub slug, and a wrong guess queries
// someone else's repository.
func TestSlugFromRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:keyolk/brd.git":       "keyolk/brd",
		"git@github.com:keyolk/brd":           "keyolk/brd",
		"https://github.com/keyolk/brd.git":   "keyolk/brd",
		"https://github.com/keyolk/brd":       "keyolk/brd",
		"ssh://git@github.com/keyolk/brd.git": "keyolk/brd",
		"  https://github.com/keyolk/brd  ":   "keyolk/brd",
		"/some/local/path":                    "local/path",
		"garbage":                             "",
		"":                                    "",
	}
	for remote, want := range cases {
		if got := SlugFromRemote(remote); got != want {
			t.Errorf("SlugFromRemote(%q) = %q, want %q", remote, got, want)
		}
	}
}

// A failing check outranks a pending one: the board surfaces what needs
// attention, and "still running" is not it.
func TestRollupPrefersTheWorstVerdict(t *testing.T) {
	check := func(status, conclusion, state string) struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
	} {
		return struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			State      string `json:"state"`
		}{status, conclusion, state}
	}
	type checkSlice = []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
	}

	cases := []struct {
		name   string
		checks checkSlice
		want   string
	}{
		{"no checks", nil, "none"},
		{"all green", checkSlice{
			check("COMPLETED", "SUCCESS", ""),
			check("COMPLETED", "SKIPPED", ""),
		}, "passing"},
		{"one failure wins", checkSlice{
			check("COMPLETED", "SUCCESS", ""),
			check("COMPLETED", "FAILURE", ""),
		}, "failing"},
		{"failure beats pending", checkSlice{
			check("IN_PROGRESS", "", ""),
			check("COMPLETED", "FAILURE", ""),
		}, "failing"},
		{"in progress", checkSlice{
			check("COMPLETED", "SUCCESS", ""),
			check("QUEUED", "", ""),
		}, "pending"},
		// StatusContext reports in `state` rather than status/conclusion.
		{"status context green", checkSlice{check("", "", "SUCCESS")}, "passing"},
		{"status context failed", checkSlice{check("", "", "FAILURE")}, "failing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rollup(ghPR{StatusCheck: tc.checks}); got != tc.want {
				t.Errorf("rollup = %q, want %q", got, tc.want)
			}
		})
	}
}

// The branch is where the ticket is guaranteed, and it exists before the PR.
func TestTicketFromPrefersTheBranch(t *testing.T) {
	if got := TicketFrom("CPLAT-11964/desc", "DINF-1 something"); got != "CPLAT-11964" {
		t.Errorf("TicketFrom = %q, want the branch's ticket", got)
	}
	if got := TicketFrom("no-ticket", "[CPLAT-8952] bump"); got != "CPLAT-8952" {
		t.Errorf("TicketFrom = %q, want the title's ticket as fallback", got)
	}
	if got := TicketFrom("main", "just a title"); got != "" {
		t.Errorf("TicketFrom = %q, want empty", got)
	}
}
