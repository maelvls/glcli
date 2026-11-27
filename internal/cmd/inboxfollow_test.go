package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// streamInboxGitLab answers "inbox --role review" for merge requests of
// bob that I review and never reviewed (review_requested since their
// creation). Merge requests and notes show up from a given request of
// the list (lists counts them; 0: from the start), so a history is
// scripted by polls.
type streamInboxGitLab struct {
	mu    sync.Mutex
	lists int
	mrs   []streamInboxMR
	// counts are the requests per merge request: "1/discussions"...
	counts map[string]int
}

type streamInboxMR struct {
	iid       int
	createdAt string
	from      int
	notes     []streamInboxNote
}

// streamInboxNote is a note mentioning me: new_replies at its time.
type streamInboxNote struct {
	id   int
	by   string
	at   string
	from int
}

func (g *streamInboxGitLab) shown(from int) bool { return from == 0 || g.lists >= from }

func (g *streamInboxGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.counts == nil {
		g.counts = map[string]int{}
	}
	find := func(iid string) *streamInboxMR {
		for i := range g.mrs {
			if fmt.Sprint(g.mrs[i].iid) == iid && g.shown(g.mrs[i].from) {
				return &g.mrs[i]
			}
		}
		return nil
	}
	mergeRequest := func(m *streamInboxMR) string {
		updatedAt := m.createdAt
		for _, n := range m.notes {
			if g.shown(n.from) && n.at > updatedAt {
				updatedAt = n.at
			}
		}
		iid := fmt.Sprint(m.iid)
		return `{"id":10` + iid + `,"iid":` + iid + `,"project_id":5,"title":"Fix it","state":"opened","author":{"username":"bob"},"sha":"abc",
			"web_url":"https://gitlab.com/group/project/-/merge_requests/` + iid + `","references":{"full":"group/project!` + iid + `"},
			"created_at":"` + m.createdAt + `","updated_at":"` + updatedAt + `"}`
	}
	switch p := r.URL.Path; {
	case p == "/api/v4/merge_requests":
		g.lists++
		var list []string
		for i := range g.mrs {
			if g.shown(g.mrs[i].from) {
				list = append(list, mergeRequest(&g.mrs[i]))
			}
		}
		fmt.Fprint(w, "["+strings.Join(list, ",")+"]")
	case waitPath.MatchString(p):
		sub := waitPath.FindStringSubmatch(p)
		g.counts[sub[1]+"/"+sub[2]]++
		m := find(sub[1])
		if m == nil {
			http.NotFound(w, r)
			return
		}
		switch sub[2] {
		case "":
			fmt.Fprint(w, mergeRequest(m))
		case "approvals":
			fmt.Fprint(w, `{}`)
		case "versions":
			fmt.Fprint(w, `[{"id":1,"head_commit_sha":"abc","created_at":"`+m.createdAt+`"}]`)
		case "discussions":
			var ds []string
			for _, n := range m.notes {
				if g.shown(n.from) {
					ds = append(ds, fmt.Sprintf(`{"id":"d%d","individual_note":true,"notes":[{"id":%d,"body":"@me can you look?","author":{"username":%q},"created_at":%q}]}`, n.id, n.id, n.by, n.at))
				}
			}
			fmt.Fprint(w, "["+strings.Join(ds, ",")+"]")
		default:
			http.NotFound(w, r)
		}
	default:
		g.counts["other"]++
		http.NotFound(w, r)
	}
}

// streamInboxHistory: !1 has waited on me since 2026-11-26 (older than
// --since: not printed); alice mentions me on !1 (list 2), !2 is opened
// (list 4), bob mentions me on !2 (list 6). Each of these changes the
// updated_at of its merge request.
func streamInboxHistory() *streamInboxGitLab {
	return &streamInboxGitLab{mrs: []streamInboxMR{
		{iid: 1, createdAt: "2026-11-26T09:00:00Z", notes: []streamInboxNote{{id: 7, by: "alice", at: "2026-11-27T12:04:30Z", from: 2}}},
		{iid: 2, createdAt: "2026-11-27T12:14:30Z", from: 4, notes: []streamInboxNote{{id: 9, by: "bob", at: "2026-11-27T12:24:30Z", from: 6}}},
	}}
}

// streamItem is what the tests read of an item.
type streamItem struct {
	Ref     string   `json:"ref"`
	Reasons []string `json:"reasons"`
	At      string   `json:"at"`
}

// items returns the items of line i, as "ref reasons at".
func (r followRun) items(t *testing.T, i int) []string {
	t.Helper()
	var items []streamItem
	if err := json.Unmarshal(r.lines[i]["items"], &items); err != nil {
		t.Fatalf("line %d: %v: %s", i, err, r.raw()[i])
	}
	var out []string
	for _, it := range items {
		out = append(out, it.Ref+" "+strings.Join(it.Reasons, ",")+" "+it.At)
	}
	return out
}

// inboxFollow runs "inbox wait --role review --interval 1m --since
// 12:00" with args against h.
func inboxFollow(t *testing.T, h http.Handler, gate *followGate, args ...string) followRun {
	t.Helper()
	rt, stdout, stderr := inboxFollowRuntime(t, h, gate)
	return runFollow(t, rt, stdout, stderr, append([]string{"inbox", "wait", "--role", "review", "--interval", "1m"}, args...)...)
}

const (
	streamItem1   = "group/project!1 review_requested,new_replies 2026-11-27T12:04:30Z"
	streamItem2   = "group/project!2 review_requested 2026-11-27T12:14:30Z"
	streamItem2Re = "group/project!2 review_requested,new_replies 2026-11-27T12:24:30Z"
)

// Three polls with something new: three items lines, the command going
// on after each, and the end line at --timeout, exit 0. !1, which waits
// on me from before --since, is printed when its reasons change, and
// never twice the same; !2 again when its reasons change.
func TestInboxFollowPrintsALinePerNewThing(t *testing.T) {
	g := streamInboxHistory()
	r := inboxFollow(t, g, nil, "--since", waitSince, "--follow", "--timeout", "45m")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if got, want := r.types(), []string{"items", "items", "items", "end"}; !slices.Equal(got, want) {
		t.Fatalf("types %v, want %v:\n%s", got, want, r.stdout)
	}
	for i, want := range [][]string{{streamItem1}, {streamItem2}, {streamItem2Re}} {
		if got := r.items(t, i); !slices.Equal(got, want) {
			t.Errorf("line %d: items %v, want %v", i, got, want)
		}
	}
	var last time.Time
	for i := range 3 {
		at, err := time.Parse(time.RFC3339, r.str(i, "checked_at"))
		if err != nil || !at.After(last) {
			t.Errorf("line %d: checked_at %q, want after %v", i, r.str(i, "checked_at"), last)
		}
		last = at
	}
	if r.str(3, "reason") != "timeout" || r.str(3, "checked_at") != r.str(2, "checked_at") {
		t.Errorf("end line %s, want timeout with the last checked_at", r.raw()[3])
	}
	if n := g.counts["other"]; n != 0 {
		t.Errorf("%d unexpected requests", n)
	}
}

// A new stream from the checked_at of line N, on the same history from
// the request of the list after line N, prints exactly the items of the
// lines after N.
func TestInboxFollowRestartReproducesTheRest(t *testing.T) {
	full := inboxFollow(t, streamInboxHistory(), nil, "--since", waitSince, "--follow", "--timeout", "45m")
	if len(full.lines) != 4 {
		t.Fatalf("want 4 lines, got:\n%s", full.stdout)
	}
	all := func(r followRun, from int) [][]string {
		var out [][]string
		for i := from; i < len(r.lines); i++ {
			if r.types()[i] == "items" {
				out = append(out, r.items(t, i))
			}
		}
		return out
	}
	// Line 0 was printed at the second request of the list, line 1 at the
	// fourth (or fifth, after a full read): start after them.
	for n, lists := range map[int]int{0: 2, 1: 4} {
		g := streamInboxHistory()
		g.lists = lists
		again := inboxFollow(t, g, nil, "--since", full.str(n, "checked_at"), "--follow", "--timeout", "45m")
		if got, want := all(again, 0), all(full, n+1); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("restarted from line %d (checked_at %s): items %v, want %v\n%s", n, full.str(n, "checked_at"), got, want, again.stdout)
		}
	}
}

// A review requested on an old merge request (it shows up in the list
// at the third poll, review_requested since its creation, long before
// --since): the stream prints it once. A plain inbox wait with the same
// --since never does: its "at" is older than --since. (A new stream
// started with a --since after it shows up does not either.)
func TestInboxFollowPrintsWhatShowsUpLateWithAnOlderAt(t *testing.T) {
	history := func() *streamInboxGitLab {
		return &streamInboxGitLab{mrs: []streamInboxMR{{iid: 3, createdAt: "2026-11-20T09:00:00Z", from: 3}}}
	}
	r := inboxFollow(t, history(), nil, "--since", waitSince, "--follow", "--timeout", "30m")
	if !slices.Equal(r.types(), []string{"items", "end"}) || !slices.Equal(r.items(t, 0), []string{"group/project!3 review_requested 2026-11-20T09:00:00Z"}) {
		t.Errorf("want !3 printed once, then the end:\n%s", r.stdout)
	}
	plain := inboxFollow(t, history(), nil, "--since", waitSince, "--timeout", "30m")
	if plain.code != 0 || !strings.Contains(plain.stdout, `"timed_out":true`) {
		t.Errorf("plain inbox wait: %s", plain.stdout)
	}
}

// The same item, with the same reasons and at, is not printed twice;
// when its reasons change, it is printed again.
func TestInboxFollowDedup(t *testing.T) {
	g := &streamInboxGitLab{mrs: []streamInboxMR{
		{iid: 1, createdAt: "2026-11-27T12:04:30Z", from: 2, notes: []streamInboxNote{{id: 7, by: "alice", at: "2026-11-27T12:30:30Z", from: 7}}},
	}}
	r := inboxFollow(t, g, nil, "--since", waitSince, "--follow", "--timeout", "45m")
	if !slices.Equal(r.types(), []string{"items", "items", "end"}) {
		t.Fatalf("want two items lines, got:\n%s", r.stdout)
	}
	if got := r.items(t, 0); !slices.Equal(got, []string{"group/project!1 review_requested 2026-11-27T12:04:30Z"}) {
		t.Errorf("line 0: %v", got)
	}
	if got := r.items(t, 1); !slices.Equal(got, []string{"group/project!1 review_requested,new_replies 2026-11-27T12:30:30Z"}) {
		t.Errorf("line 1: %v", got)
	}
}

// What is printed comes from a poll that read everything (#15's
// guarantee, per line): a push that leaves no note lands on !1, which is
// not read again, then a reply on !2 makes a line; that line has the
// push too, and the next line has neither.
func TestInboxFollowReadsEverythingBeforeEachLine(t *testing.T) {
	g := &chainGitLab{pushAt: 4, replyAt: 5}
	rt, stdout, stderr := inboxFollowRuntime(t, g, nil)
	r := runFollow(t, rt, stdout, stderr, "inbox", "wait", "--role", "review", "--interval", "1m", "--since", waitSince, "--follow", "--timeout", "1h")
	if !slices.Equal(r.types(), []string{"items", "end"}) {
		t.Fatalf("want one items line and the end, got:\n%s", r.stdout)
	}
	got := r.items(t, 0)
	slices.Sort(got)
	want := []string{"group/project!1 new_commits 2026-11-27T12:10:30Z", "group/project!2 new_replies 2026-11-27T12:16:30Z"}
	if !slices.Equal(got, want) {
		t.Errorf("items %v, want %v", got, want)
	}
}

// --timeout with nothing new: the end line alone, with --since as
// checked_at.
func TestInboxFollowTimeout(t *testing.T) {
	g := &streamInboxGitLab{mrs: []streamInboxMR{{iid: 1, createdAt: "2026-11-26T09:00:00Z"}}}
	r := inboxFollow(t, g, nil, "--since", waitSince, "--follow", "--timeout", "14m")
	if r.code != 0 || !slices.Equal(r.types(), []string{"end"}) || r.str(0, "reason") != "timeout" || r.str(0, "checked_at") != waitSince {
		t.Errorf("exit %d, want end timeout with checked_at %s:\n%s", r.code, waitSince, r.stdout)
	}
}

// An outage: one warning line however many polls fail (and the note on
// stderr), one when GitLab answers again, then the lines go on.
func TestInboxFollowOutage(t *testing.T) {
	gate := &followGate{match: isInboxList, fail: func(n int) int {
		if n == 3 || n == 4 || n == 5 {
			return http.StatusBadGateway
		}
		return 0
	}}
	r := inboxFollow(t, streamInboxHistory(), gate, "--since", waitSince, "--follow", "--timeout", "1h")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if got, want := r.types(), []string{"items", "warning", "warning", "items", "items", "end"}; !slices.Equal(got, want) {
		t.Fatalf("types %v, want %v:\n%s", got, want, r.stdout)
	}
	if !strings.HasPrefix(r.str(1, "message"), "cannot reach GitLab") || r.str(2, "message") != "GitLab answers again" || r.str(1, "checked_at") != r.str(0, "checked_at") {
		t.Errorf("warnings: %s / %s", r.raw()[1], r.raw()[2])
	}
	if strings.Count(r.stderr, "cannot reach GitLab") != 1 {
		t.Errorf("stderr: %s", r.stderr)
	}
}

// A fatal error: an error line with the last checked_at, the message on
// stderr, exit 1. Interrupted: an end line "interrupted", exit 130.
func TestInboxFollowErrorAndInterrupt(t *testing.T) {
	gate := &followGate{match: isInboxList, fail: func(n int) int {
		if n == 3 {
			return http.StatusForbidden
		}
		return 0
	}}
	r := inboxFollow(t, streamInboxHistory(), gate, "--since", waitSince, "--follow")
	if r.code != 1 || !slices.Equal(r.types(), []string{"items", "error"}) || r.str(1, "checked_at") != r.str(0, "checked_at") {
		t.Fatalf("exit %d, want 1 with an items line then an error line:\n%s", r.code, r.stdout)
	}
	if msg := r.str(1, "error"); !strings.Contains(msg, "403") || !strings.Contains(r.stderr, msg) {
		t.Errorf("error line %s; stderr %s", r.raw()[1], r.stderr)
	}

	gate = &followGate{match: isInboxList, before: interruptAt(t, 3)}
	r = inboxFollow(t, streamInboxHistory(), gate, "--since", waitSince, "--follow")
	if r.code != 130 || !slices.Equal(r.types(), []string{"items", "end"}) || r.str(1, "reason") != "interrupted" || r.str(1, "checked_at") != r.str(0, "checked_at") {
		t.Errorf("exit %d, want 130 with an items line then end interrupted:\n%s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
}
