package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/cli"
)

// waitGitLab answers "inbox wait --role review" for merge requests of bob
// that I review and never reviewed: review_requested since their creation
// on 2026-11-26, which is before the --since of the tests.
type waitGitLab struct {
	mu sync.Mutex
	// mrs is how many merge requests there are: !1, !2...
	mrs int
	// polls counts the list requests: one per poll.
	polls int
	// replyAt is the poll from which alice has replied to me on !1, at
	// 12:06, with updated_at changed (0: never).
	replyAt int
	// failing says whether the discussions of !2 fail at this poll.
	failing func(poll int) bool
	counts  map[string]int
}

var waitPath = regexp.MustCompile(`/merge_requests/(\d+)(?:/(\w+))?$`)

func (g *waitGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.counts == nil {
		g.counts = map[string]int{}
	}
	replied := func(iid string) bool { return iid == "1" && g.replyAt != 0 && g.polls >= g.replyAt }
	mergeRequest := func(iid string) string {
		updatedAt := "2026-11-26T09:00:00Z"
		if replied(iid) {
			updatedAt = "2026-11-27T12:06:00Z"
		}
		return `{"id":10` + iid + `,"iid":` + iid + `,"project_id":5,"title":"Fix it","state":"opened","author":{"username":"bob"},"sha":"abc",
			"web_url":"https://gitlab.com/group/project/-/merge_requests/` + iid + `","references":{"full":"group/project!` + iid + `"},
			"created_at":"2026-11-26T09:00:00Z","updated_at":"` + updatedAt + `"}`
	}
	switch p := r.URL.Path; {
	case p == "/api/v4/merge_requests":
		g.polls++
		g.counts["list"]++
		var list []string
		for iid := 1; iid <= g.mrs; iid++ {
			list = append(list, mergeRequest(fmt.Sprint(iid)))
		}
		fmt.Fprint(w, "["+strings.Join(list, ",")+"]")
	case waitPath.MatchString(p):
		sub := waitPath.FindStringSubmatch(p)
		g.counts[sub[1]+"/"+sub[2]]++
		switch sub[2] {
		case "":
			fmt.Fprint(w, mergeRequest(sub[1]))
		case "approvals":
			fmt.Fprint(w, `{}`)
		case "versions":
			fmt.Fprint(w, `[{"id":1,"head_commit_sha":"abc","created_at":"2026-11-26T09:00:00Z"}]`)
		case "discussions":
			switch {
			case sub[1] == "2" && g.failing != nil && g.failing(g.polls):
				http.Error(w, `{"message":"500 Internal Server Error"}`, 500)
			case replied(sub[1]):
				fmt.Fprint(w, `[{"id":"d1","individual_note":true,"notes":[{"id":7,"body":"@me can you look?","author":{"username":"alice"},"created_at":"2026-11-27T12:06:00Z"}]}]`)
			default:
				fmt.Fprint(w, `[]`)
			}
		default:
			http.NotFound(w, r)
		}
	default:
		g.counts["other"]++
		http.NotFound(w, r)
	}
}

// count returns how many times key was asked: "list", or "1/discussions",
// "1/" (the merge request itself)...
func (g *waitGitLab) count(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.counts[key]
}

// stepClock makes the clock of rt advance by a minute each time it is
// read, from 2026-11-27 12:00 UTC: Poll, which sleeps until the clock
// says the interval has passed, never sleeps for real with an interval of
// a minute, and each poll comes five minutes after the previous one (the
// clock is read five times per poll).
func stepClock(rt *cli.Runtime) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	rt.Now = func() time.Time {
		now = now.Add(time.Minute)
		return now
	}
}

type waitOutput struct {
	Items []struct {
		Ref     string    `json:"ref"`
		Reasons []string  `json:"reasons"`
		At      time.Time `json:"at"`
	} `json:"items"`
	CheckedAt time.Time `json:"checked_at"`
	TimedOut  bool      `json:"timed_out"`
	Warnings  []string  `json:"warnings"`
	Hints     []string  `json:"hints"`
}

const waitSince = "2026-11-27T12:00:00Z"

// inboxWait runs "inbox wait --role review" since 12:00 against g, with a
// timeout of 14 minutes: four polls.
func inboxWait(t *testing.T, g *waitGitLab) (waitOutput, map[string]json.RawMessage) {
	t.Helper()
	rt, stdout, stderr := fakeGitLab(t, g.ServeHTTP)
	stepClock(rt)
	if code := run(rt, []string{"inbox", "wait", "--role", "review", "--since", waitSince, "--interval", "1m", "--timeout", "14m"}); code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
	}
	var out waitOutput
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatalf("%v: %s", err, stdout.String())
	}
	if n := g.count("other"); n != 0 {
		t.Errorf("%d unexpected requests", n)
	}
	return out, raw
}

// Nothing new: the wait polls until its timeout, prints no item, and
// gives back the --since to wait again with. The merge request, which
// does not change, is read at the first two polls (the second one settles
// what is kept, see inbox.Cache) and not at the next two.
func TestInboxWaitTimesOut(t *testing.T) {
	g := &waitGitLab{mrs: 1}
	out, raw := inboxWait(t, g)
	since, _ := time.Parse(time.RFC3339, waitSince)
	if !out.TimedOut || !out.CheckedAt.Equal(since) || string(raw["items"]) != "[]" || len(out.Hints) != 1 || !strings.Contains(out.Hints[0], "--since "+waitSince) {
		t.Errorf("want timed_out, checked_at %s, no item and a hint with --since, got %+v", waitSince, out)
	}
	if polls := g.count("list"); polls != 4 {
		t.Errorf("%d polls, want 4", polls)
	}
	for _, key := range []string{"1/", "1/approvals", "1/discussions", "1/versions"} {
		if n := g.count(key); n != 2 {
			t.Errorf("%s asked %d times in 4 polls, want 2", key, n)
		}
	}
}

// Something new at the second poll: printed, and the wait ends there.
func TestInboxWaitPrintsWhatIsNew(t *testing.T) {
	g := &waitGitLab{mrs: 1, replyAt: 2}
	out, raw := inboxWait(t, g)
	since, _ := time.Parse(time.RFC3339, waitSince)
	if _, ok := raw["timed_out"]; ok || out.TimedOut {
		t.Errorf("timed_out in %+v", out)
	}
	if len(out.Items) != 1 || out.Items[0].Ref != "group/project!1" || !slices.Equal(out.Items[0].Reasons, []string{"review_requested", "new_replies"}) ||
		!out.Items[0].At.Equal(time.Date(2026, 11, 27, 12, 6, 0, 0, time.UTC)) {
		t.Errorf("want !1 with review_requested and new_replies at 12:06, got %+v", out.Items)
	}
	if !out.CheckedAt.After(since) {
		t.Errorf("checked_at = %v, want when the second poll started, after --since", out.CheckedAt)
	}
	if polls := g.count("list"); polls != 2 {
		t.Errorf("%d polls, want 2", polls)
	}
}

// Something new at the fourth poll, on a merge request that the third
// poll did not read: its updated_at changed, it is read again, and then
// everything is, before it is printed (see TestInboxWaitTwiceMissesNothing).
func TestInboxWaitReadsAgainWhatChanged(t *testing.T) {
	g := &waitGitLab{mrs: 2, replyAt: 4}
	out, _ := inboxWait(t, g)
	if out.TimedOut || len(out.Items) != 1 || out.Items[0].Ref != "group/project!1" || !slices.Contains(out.Items[0].Reasons, "new_replies") {
		t.Errorf("want !1 with new_replies, got %+v", out)
	}
	if lists := g.count("list"); lists != 5 {
		t.Errorf("the list asked %d times, want 5: four polls, and the full read before printing", lists)
	}
	// !1: polls 1, 2 and 4, and the full read. !2, unchanged: polls 1
	// and 2, and the full read.
	if one, two := g.count("1/discussions"), g.count("2/discussions"); one != 4 || two != 3 {
		t.Errorf("discussions of !1 asked %d times and of !2 %d times, want 4 and 3", one, two)
	}
}

// A merge request that cannot be read is warned about once, for the last
// poll: not once per poll, and not when a later poll could read it.
func TestInboxWaitWarnings(t *testing.T) {
	g := &waitGitLab{mrs: 2, failing: func(int) bool { return true }}
	out, _ := inboxWait(t, g)
	if !out.TimedOut || len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "merge_requests/2") {
		t.Errorf("failing at each poll: want timed_out and one warning about !2, got %+v", out)
	}
	if n := g.count("2/discussions"); n != 4 {
		t.Errorf("failing at each poll: discussions of !2 asked %d times, want 4 (an error is not kept)", n)
	}

	g = &waitGitLab{mrs: 2, failing: func(poll int) bool { return poll == 1 }}
	out, _ = inboxWait(t, g)
	if !out.TimedOut || len(out.Warnings) != 0 {
		t.Errorf("failing at the first poll only: want timed_out and no warning, got %+v", out)
	}
}
