package cmd

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// A reason that goes away is not news: the reply that made !1 new is
// deleted (its "at" goes back to the review request, before --since),
// and nothing is printed. When the reply comes back, the reason is new
// again: printed.
func TestInboxFollowReasonGoneIsNotNews(t *testing.T) {
	g := &streamInboxGitLab{mrs: []streamInboxMR{{iid: 1, createdAt: "2026-11-26T09:00:00Z", notes: []streamInboxNote{
		{id: 7, by: "alice", at: "2026-11-27T12:04:30Z", from: 2, until: 4},
		{id: 7, by: "alice", at: "2026-11-27T12:04:30Z", from: 6},
	}}}}
	r := inboxFollow(t, g, nil, "--since", waitSince, "--follow", "--timeout", "45m")
	if !slices.Equal(r.types(), []string{"items", "items", "end"}) {
		t.Fatalf("want two items lines (the reply, then the reply back), got:\n%s", r.stdout)
	}
	for i := range 2 {
		if got := r.items(t, i); !slices.Equal(got, []string{streamItem1}) {
			t.Errorf("line %d: %v, want %s", i, got, streamItem1)
		}
	}
}

// A merge request that cannot be read at the first poll gets the
// --since rule at its first successful read: waiting since before
// --since, it is not printed.
func TestInboxFollowSetsAsideWhatTheFirstPollCouldNotRead(t *testing.T) {
	g := &streamInboxGitLab{mrs: []streamInboxMR{{iid: 1, createdAt: "2026-11-27T10:40:00Z", failBefore: 2}}}
	r := inboxFollow(t, g, nil, "--since", waitSince, "--follow", "--timeout", "30m")
	if !slices.Equal(r.types(), []string{"end"}) {
		t.Errorf("want the end line alone, got:\n%s", r.stdout)
	}
	if g.counts["1/discussions"] < 2 {
		t.Errorf("the discussions of !1 were asked %d times: never read", g.counts["1/discussions"])
	}
}

// An item newer than --since, there from the first poll: printed by the
// first poll.
func TestInboxFollowPrintsWhatIsNewerThanSinceAtStart(t *testing.T) {
	g := &streamInboxGitLab{mrs: []streamInboxMR{{iid: 1, createdAt: "2026-11-27T12:00:30Z"}}}
	r := inboxFollow(t, g, nil, "--since", waitSince, "--follow", "--timeout", "14m")
	if !slices.Equal(r.types(), []string{"items", "end"}) || !slices.Equal(r.items(t, 0), []string{"group/project!1 review_requested 2026-11-27T12:00:30Z"}) {
		t.Errorf("want !1 printed at once, got:\n%s", r.stdout)
	}
}

// A new reply under the same reasons moves "at": printed again.
func TestInboxFollowPrintsANewReplyUnderTheSameReasons(t *testing.T) {
	g := &streamInboxGitLab{mrs: []streamInboxMR{{iid: 1, createdAt: "2026-11-26T09:00:00Z", notes: []streamInboxNote{
		{id: 7, by: "alice", at: "2026-11-27T12:04:30Z", from: 2},
		{id: 8, by: "bob", at: "2026-11-27T12:14:30Z", from: 4},
	}}}}
	r := inboxFollow(t, g, nil, "--since", waitSince, "--follow", "--timeout", "30m")
	if !slices.Equal(r.types(), []string{"items", "items", "end"}) {
		t.Fatalf("want two items lines, got:\n%s", r.stdout)
	}
	if got := r.items(t, 1); !slices.Equal(got, []string{"group/project!1 review_requested,new_replies 2026-11-27T12:14:30Z"}) {
		t.Errorf("line 1: %v", got)
	}
}

// The full read before printing does not double the warnings of the
// poll: !2 cannot be read, !1 is kept, a reply on !3 makes a line.
func TestInboxFollowWarningsOncePerLine(t *testing.T) {
	g := &streamInboxGitLab{mrs: []streamInboxMR{
		{iid: 1, createdAt: "2026-11-26T09:00:00Z"},
		{iid: 2, createdAt: "2026-11-26T09:00:00Z", failAlways: true},
		{iid: 3, createdAt: "2026-11-26T09:00:00Z", notes: []streamInboxNote{{id: 7, by: "alice", at: "2026-11-27T12:09:30Z", from: 3}}},
	}}
	r := inboxFollow(t, g, nil, "--since", waitSince, "--follow", "--timeout", "20m")
	if !slices.Equal(r.types(), []string{"items", "end"}) {
		t.Fatalf("want one items line, got:\n%s", r.stdout)
	}
	var warnings []string
	_ = json.Unmarshal(r.lines[0]["warnings"], &warnings)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "merge_requests/2") {
		t.Errorf("warnings %q, want one about !2", warnings)
	}
	if g.lists < 4 {
		t.Errorf("the list was asked %d times: no full read before printing", g.lists)
	}
}

// GitLab answers one poll whose forced full read then fails, and the
// outage goes on until --timeout: no "GitLab answers again" line, since
// GitLab never answered a whole poll.
func TestInboxFollowNoAnswersAgainWhenTheFullReadFails(t *testing.T) {
	g := &streamInboxGitLab{mrs: []streamInboxMR{
		{iid: 1, createdAt: "2026-11-26T09:00:00Z"},
		// From the poll after the first failure: something new, which
		// asks for a full read.
		{iid: 2, createdAt: "2026-11-26T09:00:00Z", notes: []streamInboxNote{{id: 7, by: "alice", at: "2026-11-27T12:09:30Z", from: 3}}},
	}}
	gate := &followGate{match: isInboxList, fail: func(n int) int {
		if n == 3 || n >= 5 {
			return http.StatusBadGateway
		}
		return 0
	}}
	r := inboxFollow(t, g, gate, "--since", waitSince, "--follow", "--timeout", "40m")
	if !slices.Equal(r.types(), []string{"warning", "end"}) || !strings.HasPrefix(r.str(0, "message"), "cannot reach GitLab") || r.str(1, "reason") != "timeout" {
		t.Errorf("want one outage warning then the end, got:\n%s", r.stdout)
	}
	if g.lists != 3 {
		t.Errorf("the list was answered %d times, want 3 (the fourth poll answered, its full read did not)", g.lists)
	}
}
