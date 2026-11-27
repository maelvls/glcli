package cmd

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/gitlab"
)

// mrFollowHistory is a scripted history of g/p!1, one state per fetch,
// where the polls 3, 5 and 7 would each end mr wait: my approval (which
// does not end it) then bob's, carol's, and bob's merge. Each
// state that ends the wait is fetched twice, so that a wait started
// from the cursor of the line it printed sees it first (see
// TestMRFollowRestartReproducesTheRest).
func mrFollowHistory() []mrWaitState {
	opened := mrWaitOpened()
	mine := opened
	mine.Approvals.ApprovedBy = append(mine.Approvals.ApprovedBy, struct {
		User gitlab.User `json:"user"`
	}{gitlab.User{Username: "me"}})
	bob := mine.note(12, "bob", 3, "done, have a look", false)
	carol := bob.note(13, "carol", 4, "lgtm", false)
	merged := carol.state("merged")
	merged.MR.MergeUser = &gitlab.User{Username: "bob"}
	return []mrWaitState{opened, mine, bob, bob, carol, carol, merged}
}

// eventKinds returns the kinds of the events of line i.
func (r followRun) eventKinds(t *testing.T, i int) []string {
	t.Helper()
	var events []mrWaitEvent
	if err := json.Unmarshal(r.lines[i]["events"], &events); err != nil {
		t.Fatalf("line %d: %v: %s", i, err, r.raw()[i])
	}
	return mrWaitKinds(events)
}

// mrFollowStart returns a fake playing nothing yet and the cursor of mr
// get on the opened merge request.
func mrFollowStart(t *testing.T) (*mrWaitFake, string) {
	t.Helper()
	f := newMRWaitFake(t, "18.5.1-ee")
	f.play(mrWaitOpened())
	return f, f.get().Cursor
}

// Three polls that would each end mr wait: three events lines, the
// command going on after each, then the end line once merged, exit 0.
// My own approval does not end the wait: it comes with bob's, in the same
// line, as mr wait prints it. Each line's cursor is the next one's start.
func TestMRFollowPrintsALinePerTriggeringPoll(t *testing.T) {
	f, start := mrFollowStart(t)
	f.play(mrFollowHistory()...)
	r := mrFollow(t, f, nil, "--cursor", start, "--follow")
	if r.code != 0 {
		t.Fatalf("exit %d: %s %s", r.code, r.stderr, r.stdout)
	}
	if got, want := r.types(), []string{"events", "events", "events", "end"}; !slices.Equal(got, want) {
		t.Fatalf("types %v, want %v:\n%s", got, want, r.stdout)
	}
	for i, want := range [][]string{{"comment", "approved"}, {"comment"}, {"state"}} {
		if got := r.eventKinds(t, i); !slices.Equal(got, want) {
			t.Errorf("line %d: events %v, want %v: %s", i, got, want, r.raw()[i])
		}
	}
	for i, want := range []string{`["comment"]`, `["comment"]`, `["state"]`} {
		if got := string(r.lines[i]["triggered_by"]); got != want {
			t.Errorf("line %d: triggered_by %s, want %s", i, got, want)
		}
	}
	if !strings.Contains(r.raw()[0], `"mine":true`) || !strings.Contains(r.raw()[0], `"kind":"approved"`) {
		t.Errorf("line 0 lacks my own approval: %s", r.raw()[0])
	}
	if r.str(3, "reason") != "merged" {
		t.Errorf("end line %s, want reason merged", r.raw()[3])
	}
	// The cursor is at the top of each line, and not in mr.
	cursors := map[string]bool{start: true}
	for i, l := range r.lines {
		c := r.str(i, "cursor")
		if c == "" || strings.Contains(string(l["mr"]), `"cursor"`) {
			t.Errorf("line %d: cursor %q, mr %s", i, c, l["mr"])
		}
		if i < 3 && cursors[c] {
			t.Errorf("line %d: cursor %q already printed", i, c)
		}
		cursors[c] = true
	}
	if r.str(3, "cursor") != r.str(2, "cursor") {
		t.Errorf("the end line's cursor is not the last events line's")
	}
	// Each line comes at the poll that would have ended mr wait.
	if got := f.fetches(); got != 7 {
		t.Errorf("fetched %d times, want 7", got)
	}
	// From the last cursor, a plain mr wait sees nothing new.
	f.play(mrFollowHistory()[6])
	if again := f.wait(r.str(3, "cursor"), "--timeout", "1m"); len(again.Events) != 0 {
		t.Errorf("mr wait from the end line's cursor: %s; want no event", again.raw)
	}
}

// A new stream from the cursor of line N, on the same history from the
// poll after line N, prints exactly the lines after N: no event lost,
// none printed twice.
func TestMRFollowRestartReproducesTheRest(t *testing.T) {
	f, start := mrFollowStart(t)
	history := mrFollowHistory()
	f.play(history...)
	full := mrFollow(t, f, nil, "--cursor", start, "--follow")
	if len(full.lines) != 4 {
		t.Fatalf("want 4 lines, got:\n%s", full.stdout)
	}
	// Line 0 was printed at fetch 3 (history[2]), line 1 at fetch 5.
	for n, from := range map[int]int{0: 3, 1: 5} {
		f.play(history[from:]...)
		again := mrFollow(t, f, nil, "--cursor", full.str(n, "cursor"), "--follow")
		if got, want := again.raw(), full.raw()[n+1:]; !slices.Equal(got, want) {
			t.Errorf("restarted from line %d:\n%s\nwant:\n%s", n, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
}

// --timeout covers the whole stream: the lines printed until then, then
// an end line "timeout" with the cursor of the last line, exit 0. With no
// line, the end line carries the cursor it started from.
func TestMRFollowTimeout(t *testing.T) {
	f, start := mrFollowStart(t)
	history := mrFollowHistory()
	f.play(history[:4]...)
	r := mrFollow(t, f, nil, "--cursor", start, "--follow", "--timeout", "2m")
	if r.code != 0 || !slices.Equal(r.types(), []string{"events", "end"}) || r.str(1, "reason") != "timeout" || r.str(1, "cursor") != r.str(0, "cursor") {
		t.Errorf("exit %d, want an events line then end timeout with its cursor:\n%s", r.code, r.stdout)
	}

	f.play(history[0])
	r = mrFollow(t, f, nil, "--cursor", start, "--follow", "--timeout", "1m")
	if r.code != 0 || !slices.Equal(r.types(), []string{"end"}) || r.str(0, "reason") != "timeout" || r.str(0, "cursor") != start {
		t.Errorf("exit %d, want end timeout with the cursor it started from (%s):\n%s", r.code, start, r.stdout)
	}
}

// A push whose patch ID GitLab has not computed yet holds the events
// that came with it for at most two polls (see AwaitsPatchID); then they
// come out together in one line, and the stream goes on. A timeout
// while it waits ends on the last good cursor, the one it started from.
func TestMRFollowWaitsForAPatchIDTwoPollsAtMost(t *testing.T) {
	f, start := mrFollowStart(t)
	opened := mrWaitOpened()
	// alice pushes head2 (no patch ID yet) and bob comments, at once.
	pushed := opened.push(2, "head2", "base2", "", 5).note(12, "bob", 6, "pushed a fix", false)
	later := pushed.note(2000, "carol", 7, "nice", false) // note IDs only grow: above the push note
	// Fetch 1 sees nothing, 2 and 3 wait for the patch ID, 4 prints: had it
	// waited longer, carol's comment (from fetch 5) would be in line 0.
	f.play(opened, pushed, pushed, pushed, later, later)
	r := mrFollow(t, f, nil, "--cursor", start, "--follow", "--timeout", "20m")
	if !slices.Equal(r.types(), []string{"events", "events", "end"}) {
		t.Fatalf("want two events lines and the end, got:\n%s", r.stdout)
	}
	if got := r.eventKinds(t, 0); !slices.Equal(got, []string{"push", "comment"}) {
		t.Errorf("line 0: events %v, want the push and bob's comment together", got)
	}
	if got := r.eventKinds(t, 1); !slices.Equal(got, []string{"comment"}) {
		t.Errorf("line 1: events %v, want carol's comment", got)
	}

	// The timeout comes while the push waits for its patch ID: no line,
	// and the end line has the cursor it started from, not an empty one.
	f, start = mrFollowStart(t)
	f.play(opened, pushed)
	r = mrFollow(t, f, nil, "--cursor", start, "--follow", "--timeout", "20s")
	if !slices.Equal(r.types(), []string{"end"}) || r.str(0, "reason") != "timeout" || r.str(0, "cursor") != start {
		t.Errorf("want end timeout with the cursor it started from, got:\n%s", r.stdout)
	}
	if n := f.fetches(); n < 2 || n > 3 {
		t.Errorf("fetched %d times: the timeout should come while the push waits for its patch ID", n)
	}
}

// Closed: the last events line, the end line "closed", exit 0. Already
// closed from the cursor: the end line alone.
func TestMRFollowEndsWhenClosed(t *testing.T) {
	f, start := mrFollowStart(t)
	opened := mrWaitOpened()
	closed := opened.state("closed")
	closed.MR.ClosedBy = &gitlab.User{Username: "alice"}
	f.play(opened, closed)
	r := mrFollow(t, f, nil, "--cursor", start, "--follow")
	if r.code != 0 || !slices.Equal(r.types(), []string{"events", "end"}) || r.str(1, "reason") != "closed" {
		t.Fatalf("exit %d, want events then end closed:\n%s", r.code, r.stdout)
	}
	f.play(closed)
	r = mrFollow(t, f, nil, "--cursor", r.str(1, "cursor"), "--follow")
	if r.code != 0 || !slices.Equal(r.types(), []string{"end"}) || r.str(0, "reason") != "closed" {
		t.Errorf("exit %d, want end closed alone:\n%s", r.code, r.stdout)
	}
}

// An outage: one warning line however many polls fail (and the note on
// stderr, as without --follow), one when GitLab answers again, then the
// lines go on.
func TestMRFollowOutage(t *testing.T) {
	f, start := mrFollowStart(t)
	f.play(mrFollowHistory()...)
	gate := &followGate{match: isMRFetch, fail: func(n int) int {
		if n >= 4 && n <= 6 {
			return http.StatusServiceUnavailable
		}
		return 0
	}}
	r := mrFollow(t, f, gate, "--cursor", start, "--follow")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if got, want := r.types(), []string{"events", "warning", "warning", "events", "events", "end"}; !slices.Equal(got, want) {
		t.Fatalf("types %v, want %v:\n%s", got, want, r.stdout)
	}
	if !strings.HasPrefix(r.str(1, "message"), "cannot reach GitLab") || r.str(2, "message") != "GitLab answers again" {
		t.Errorf("warnings: %s / %s", r.raw()[1], r.raw()[2])
	}
	if r.str(1, "cursor") != r.str(0, "cursor") || r.str(2, "cursor") != r.str(0, "cursor") {
		t.Errorf("the warning lines do not carry the last cursor")
	}
	if strings.Count(r.stderr, "cannot reach GitLab") != 1 || !strings.Contains(r.stderr, "GitLab answers again") {
		t.Errorf("stderr: %s", r.stderr)
	}
}

// A fatal error: an error line on stdout with the last cursor, then the
// usual message on stderr and exit 1.
func TestMRFollowFatalError(t *testing.T) {
	f, start := mrFollowStart(t)
	f.play(mrFollowHistory()...)
	gate := &followGate{match: isMRFetch, fail: func(n int) int {
		if n == 4 {
			return http.StatusForbidden
		}
		return 0
	}}
	r := mrFollow(t, f, gate, "--cursor", start, "--follow")
	if r.code != 1 || !slices.Equal(r.types(), []string{"events", "error"}) {
		t.Fatalf("exit %d, want 1 with an events line then an error line:\n%s", r.code, r.stdout)
	}
	if msg := r.str(1, "error"); !strings.Contains(msg, "403") || !strings.Contains(r.stderr, msg) || r.str(1, "cursor") != r.str(0, "cursor") {
		t.Errorf("error line %s; stderr %s", r.raw()[1], r.stderr)
	}

	// A usage error, before anything is polled: the error line too, with
	// the cursor given, and exit 2.
	r = mrFollow(t, f, nil, "--cursor", start, "--follow", "--on", "nothing")
	if r.code != 2 || !slices.Equal(r.types(), []string{"error"}) || r.str(0, "cursor") != start || !strings.Contains(r.stderr, r.str(0, "error")) {
		t.Errorf("exit %d, want 2 with an error line:\n%s", r.code, r.stdout)
	}
}

// interruptAt returns a gate hook that interrupts glcli (SIGINT, as ^C)
// at the nth request it matches, and holds that request until glcli
// gives up on it.
func interruptAt(t *testing.T, at int) func(n int, r *http.Request) {
	return func(n int, r *http.Request) {
		if n != at {
			return
		}
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
			t.Errorf("kill: %v", err)
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
			t.Errorf("the request was not cancelled after SIGINT")
		}
	}
}

// Interrupted: an end line "interrupted" with the cursor of the last
// line, and exit 130.
func TestMRFollowInterrupted(t *testing.T) {
	f, start := mrFollowStart(t)
	f.play(mrFollowHistory()...)
	gate := &followGate{match: isMRFetch, before: interruptAt(t, 4)}
	r := mrFollow(t, f, gate, "--cursor", start, "--follow")
	if r.code != 130 || !slices.Equal(r.types(), []string{"events", "end"}) || r.str(1, "reason") != "interrupted" || r.str(1, "cursor") != r.str(0, "cursor") {
		t.Errorf("exit %d, want 130 with an events line, then end interrupted with its cursor:\n%s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
}
