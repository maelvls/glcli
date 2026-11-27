package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"
)

// chainGitLab has two merge requests of bob, !1 and !2, that I review and
// commented on 2026-11-26: nothing waits on me. Things happen when the
// list has been asked a given number of times (0: never).
type chainGitLab struct {
	mu    sync.Mutex
	lists int
	// pushAt: from then on, !1 has a second diff version, created at
	// 12:10:30, with no "added N commits" note and the same updated_at (a
	// force push to an ancestor).
	pushAt int
	// replyAt: from then on, bob has replied to me on !2 at 12:16:30, with
	// updated_at changed. replyGoneAt: from then on, the reply is deleted.
	replyAt, replyGoneAt int
	counts               map[string]int
}

func (g *chainGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.counts == nil {
		g.counts = map[string]int{}
	}
	if r.URL.Path == "/api/v4/merge_requests" {
		g.lists++
	}
	from := func(n int) bool { return n != 0 && g.lists >= n }
	pushed := func(iid string) bool { return iid == "1" && from(g.pushAt) }
	touched := func(iid string) bool { return iid == "2" && from(g.replyAt) }
	mergeRequest := func(iid string) string {
		sha, updatedAt := "abc", "2026-11-26T09:30:00Z"
		if pushed(iid) {
			sha = "old"
		}
		if touched(iid) {
			updatedAt = "2026-11-27T12:16:30Z"
		}
		return `{"id":10` + iid + `,"iid":` + iid + `,"project_id":5,"title":"Fix it","state":"opened","author":{"username":"bob"},"sha":"` + sha + `",
			"web_url":"https://gitlab.com/group/project/-/merge_requests/` + iid + `","references":{"full":"group/project!` + iid + `"},
			"created_at":"2026-11-26T09:00:00Z","updated_at":"` + updatedAt + `"}`
	}
	switch p := r.URL.Path; {
	case p == "/api/v4/merge_requests":
		g.counts["list"]++
		fmt.Fprint(w, "["+mergeRequest("1")+","+mergeRequest("2")+"]")
	case waitPath.MatchString(p):
		sub := waitPath.FindStringSubmatch(p)
		g.counts[sub[1]+"/"+sub[2]]++
		switch sub[2] {
		case "":
			fmt.Fprint(w, mergeRequest(sub[1]))
		case "approvals":
			fmt.Fprint(w, `{}`)
		case "versions":
			versions := `{"id":1,"head_commit_sha":"abc","created_at":"2026-11-26T09:00:00Z"}`
			if pushed(sub[1]) {
				versions = `{"id":2,"head_commit_sha":"old","created_at":"2026-11-27T12:10:30Z"},` + versions
			}
			fmt.Fprint(w, "["+versions+"]")
		case "discussions":
			notes := `{"id":7,"body":"nit","author":{"username":"me"},"created_at":"2026-11-26T09:30:00Z"}`
			if touched(sub[1]) && !from(g.replyGoneAt) {
				notes += `,{"id":8,"body":"done","author":{"username":"bob"},"created_at":"2026-11-27T12:16:30Z"}`
			}
			fmt.Fprint(w, `[{"id":"d1","notes":[`+notes+`]}]`)
		default:
			http.NotFound(w, r)
		}
	default:
		g.counts["other"]++
		http.NotFound(w, r)
	}
}

func (g *chainGitLab) count(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.counts[key]
}

// wait runs "inbox wait --role review --since since" against g, the
// clock starting at start and advancing by a minute each time it is read
// (see stepClock).
func (g *chainGitLab) wait(t *testing.T, start time.Time, since, timeout string) waitOutput {
	t.Helper()
	rt, stdout, stderr := fakeGitLab(t, g.ServeHTTP)
	now := start
	rt.Now = func() time.Time {
		now = now.Add(time.Minute)
		return now
	}
	if code := run(rt, []string{"inbox", "wait", "--role", "review", "--since", since, "--interval", "1m", "--timeout", timeout}); code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
	}
	var out waitOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, stdout.String())
	}
	if n := g.count("other"); n != 0 {
		t.Errorf("%d unexpected requests", n)
	}
	return out
}

// Two waits in a row, the second since the checked_at of the first, as
// the help says to. While the first one waits, a push that leaves no
// note lands on !1, which is not read again; then a reply on !2 ends the
// wait. The push must not be lost between the two: everything is read
// before the first wait prints, so that its checked_at covers !1 too.
func TestInboxWaitTwiceMissesNothing(t *testing.T) {
	// Polls 1 and 2 read everything, 3 and 4 only the list. The push is
	// there from poll 4, the reply from poll 5. (Poll 6 would read
	// everything again: the polls are five minutes apart, and what is kept
	// would be 20 minutes old.)
	g := &chainGitLab{pushAt: 4, replyAt: 5}
	noon := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	first := g.wait(t, noon, waitSince, "2h")

	printed := map[string][]string{}
	for _, it := range first.Items {
		printed[it.Ref] = it.Reasons
		want := map[string]time.Time{
			"group/project!1": time.Date(2026, 11, 27, 12, 10, 30, 0, time.UTC),
			"group/project!2": time.Date(2026, 11, 27, 12, 16, 30, 0, time.UTC),
		}[it.Ref]
		if !it.At.Equal(want) {
			t.Errorf("first wait: %s at %v, want %v", it.Ref, it.At, want)
		}
	}
	if first.TimedOut || !slices.Equal(printed["group/project!2"], []string{"new_replies"}) {
		t.Errorf("first wait: want !2 with new_replies, got %+v", first)
	}
	if lists := g.count("list"); lists != 6 {
		t.Errorf("first wait: the list asked %d times, want 6: five polls, and the full read before printing", lists)
	}
	if n := g.count("1/versions"); n != 3 {
		t.Errorf("first wait: versions of !1 asked %d times, want 3: polls 1 and 2, and the full read before printing", n)
	}

	// The second wait starts where the first one ended.
	second := g.wait(t, first.CheckedAt, first.CheckedAt.Format(time.RFC3339), "14m")
	for _, it := range second.Items {
		printed[it.Ref] = it.Reasons
	}
	if !slices.Equal(printed["group/project!1"], []string{"new_commits"}) {
		t.Errorf("the push on !1 was printed by neither wait: first %+v, second %+v", first.Items, second.Items)
	}
	if !second.TimedOut || len(second.Items) != 0 {
		t.Errorf("second wait: want timed_out and no item, both were printed by the first, got %+v", second)
	}
}

// What was new is gone when everything is read (the reply was deleted):
// nothing is printed, and the wait goes on.
func TestInboxWaitGoesOnWhenNothingIsNewAfterAll(t *testing.T) {
	g := &chainGitLab{replyAt: 3, replyGoneAt: 4}
	out := g.wait(t, time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC), waitSince, "30m")
	if !out.TimedOut || len(out.Items) != 0 {
		t.Errorf("want timed_out and no item, got %+v", out)
	}
	// Polls 1, 2, 3, the full read, and at least one more poll.
	if lists := g.count("list"); lists < 5 {
		t.Errorf("the list asked %d times, want at least 5", lists)
	}
}

// Something new at a poll that read everything anyway (the first one):
// printed as it is, with no second read.
func TestInboxWaitDoesNotReadTwiceWhatItJustRead(t *testing.T) {
	g := &chainGitLab{pushAt: 1, replyAt: 1}
	out := g.wait(t, time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC), waitSince, "14m")
	if out.TimedOut || len(out.Items) != 2 {
		t.Errorf("want !1 and !2, got %+v", out)
	}
	if lists, versions := g.count("list"), g.count("1/versions"); lists != 1 || versions != 1 {
		t.Errorf("the list asked %d times and the versions of !1 %d times, want 1 and 1", lists, versions)
	}
}
