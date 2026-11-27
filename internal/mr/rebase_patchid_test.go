package mr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
)

// differFake answers what Differ.Events asks GitLab beyond the snapshot:
// its version and comparisons. It records every request.
type differFake struct {
	mu sync.Mutex
	// release is the version GitLab says it is; with releaseStatus set,
	// GET /metadata fails with it.
	release       string
	releaseStatus int
	// compare maps "from..to" to the raw JSON answer of the comparison;
	// a pair that is absent answers 404. With compareStatus set, every
	// comparison fails with it.
	compare       map[string]string
	compareStatus int
	// asked lists the requests: "metadata", "hash from..to" (a direct
	// comparison, for the diff) and "commits from..to".
	asked []string
}

func (f *differFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/api/v4/metadata":
		f.asked = append(f.asked, "metadata")
		if f.releaseStatus != 0 {
			http.Error(w, `{"message":"failed"}`, f.releaseStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"version": f.release})
	case strings.HasSuffix(r.URL.Path, "/repository/compare"):
		q := r.URL.Query()
		key := q.Get("from") + ".." + q.Get("to")
		what := "commits "
		if q.Get("straight") == "true" {
			what = "hash "
		}
		f.asked = append(f.asked, what+key)
		if f.compareStatus != 0 {
			http.Error(w, `{"message":"failed"}`, f.compareStatus)
			return
		}
		answer, ok := f.compare[key]
		if !ok {
			http.Error(w, `{"message":"404 Not found"}`, 404)
			return
		}
		_, _ = w.Write([]byte(answer))
	default:
		f.asked = append(f.asked, "unexpected "+r.URL.String())
		http.NotFound(w, r)
	}
}

// requests returns what was asked so far, and forgets it.
func (f *differFake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.asked
	f.asked = nil
	return out
}

// differHosts numbers the fake hosts: what a host answered to
// GET /metadata is remembered for as long as the process lives.
var differHosts atomic.Int64

func (f *differFake) client(t *testing.T) *gitlab.Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := gitlab.New(config.Host{Name: fmt.Sprintf("differ-%d", differHosts.Add(1)), Token: "x"})
	c.BaseURL = srv.URL
	return c
}

// comparison is the answer of the compare API for one file, a.go, with
// one commit.
func comparison(diff string) string {
	b, _ := json.Marshal(map[string]any{
		"commits": []map[string]string{{"short_id": "c1", "title": "a commit"}},
		"diffs":   []map[string]any{{"old_path": "a.go", "new_path": "a.go", "a_mode": "100644", "b_mode": "100644", "diff": diff}},
	})
	return string(b)
}

const (
	patchA = "d504412d5b6e6739647e752aff8e468dde093f2f"
	patchB = "72c30d1f0115fc1d2bb0b29b24dc2982cbcdfd32"
)

// pushed is snapshot() after a push from head1 to head2, and the cursor
// taken before it: the patch IDs are those GitLab gave for each head.
func pushed(before, after string) (Cursor, *Snapshot) {
	s := snapshot()
	prev := NewCursor(s)
	prev.SHA, prev.Base, prev.Version, prev.Patch = "head1", "base1", 1, before
	s.Versions[0].PatchIDSHA = after
	return prev, s
}

// onlyPush runs Events and returns the push, which must be the only event.
func onlyPush(t *testing.T, d *Differ, prev Cursor, s *Snapshot) Event {
	t.Helper()
	events, err := d.Events(context.Background(), prev, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "push" {
		t.Fatalf("events = %+v, want one push", events)
	}
	return events[0]
}

func rebaseOnly(e Event) string {
	switch {
	case e.RebaseOnly == nil:
		return "unknown"
	case *e.RebaseOnly:
		return "true"
	}
	return "false"
}

const (
	sameDiff  = "@@ -1,2 +1,2 @@\n ctx\n-old\n+new\n"
	otherDiff = "@@ -1,2 +1,2 @@\n ctx\n-old\n+newer\n"
)

// On GitLab 18.2 or newer, GitLab's patch IDs decide, and the two diffs
// are never asked for. Otherwise glcli compares them itself.
func TestRebaseOnlyFromPatchIDs(t *testing.T) {
	hashes := []string{"hash base1..head1", "hash base2..head2"}
	commits := []string{"commits base2..head2"}
	join := func(lists ...[]string) []string { return slices.Concat(lists...) }
	tests := []struct {
		name          string
		release       string
		status        int
		before, after string
		// newDiff is what base2..head2 is; base1..head1 is sameDiff.
		newDiff string
		want    string
		asked   []string
	}{
		{name: "18.2, same patch ID", release: "18.2.0", before: patchA, after: patchA, newDiff: otherDiff,
			want: "true", asked: []string{"metadata"}},
		{name: "18.5.1-ee, same patch ID", release: "18.5.1-ee", before: patchA, after: patchA, newDiff: otherDiff,
			want: "true", asked: []string{"metadata"}},
		{name: "19.0, same patch ID", release: "19.0.2", before: patchA, after: patchA, newDiff: otherDiff,
			want: "true", asked: []string{"metadata"}},
		{name: "18.5.1-ee, another patch ID", release: "18.5.1-ee", before: patchA, after: patchB, newDiff: sameDiff,
			want: "false", asked: join([]string{"metadata"}, commits)},

		// Before 18.2 the patch ID ignores whitespace: the diffs decide.
		{name: "18.1, same patch ID, same diff", release: "18.1.6-ee", before: patchA, after: patchA, newDiff: sameDiff,
			want: "true", asked: join([]string{"metadata"}, hashes)},
		{name: "18.1, same patch ID, another diff", release: "18.1.6-ee", before: patchA, after: patchA, newDiff: otherDiff,
			want: "false", asked: join([]string{"metadata"}, hashes, commits)},
		{name: "17.11, same patch ID, another diff", release: "17.11.4", before: patchA, after: patchA, newDiff: otherDiff,
			want: "false", asked: join([]string{"metadata"}, hashes, commits)},
		{name: "a version that cannot be read", release: "main", before: patchA, after: patchA, newDiff: otherDiff,
			want: "false", asked: join([]string{"metadata"}, hashes, commits)},
		{name: "GitLab does not say its version", status: 500, before: patchA, after: patchA, newDiff: otherDiff,
			want: "false", asked: join([]string{"metadata"}, hashes, commits)},

		// A patch ID missing on either side: the diffs decide, and the
		// version is not asked.
		{name: "a cursor without a patch ID, another diff", release: "18.5.1-ee", after: patchA, newDiff: otherDiff,
			want: "false", asked: join(hashes, commits)},
		{name: "a cursor without a patch ID, same diff", release: "18.5.1-ee", after: patchA, newDiff: sameDiff,
			want: "true", asked: hashes},
		{name: "a version without a patch ID, another diff", release: "18.5.1-ee", before: patchA, newDiff: otherDiff,
			want: "false", asked: join(hashes, commits)},
		{name: "a version without a patch ID, same diff", release: "18.5.1-ee", before: patchA, newDiff: sameDiff,
			want: "true", asked: hashes},
		{name: "no patch ID at all, another diff", release: "18.5.1-ee", newDiff: otherDiff,
			want: "false", asked: join(hashes, commits)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &differFake{release: tt.release, releaseStatus: tt.status, compare: map[string]string{
				"base1..head1": comparison(sameDiff),
				"base2..head2": comparison(tt.newDiff),
			}}
			prev, s := pushed(tt.before, tt.after)
			e := onlyPush(t, &Differ{Client: f.client(t), People: People{Me: "me"}}, prev, s)
			if got := rebaseOnly(e); got != tt.want {
				t.Errorf("rebase_only = %s, want %s", got, tt.want)
			}
			if got := f.requests(); !slices.Equal(got, tt.asked) {
				t.Errorf("asked %v, want %v", got, tt.asked)
			}
		})
	}
}

// A wait polls for as long as a rebase is pending: the version is asked
// once, and nothing is compared.
func TestRebaseOnlyFromPatchIDsWhilePolling(t *testing.T) {
	f := &differFake{release: "18.5.1-ee"}
	d := &Differ{Client: f.client(t), People: People{Me: "me"}}
	prev, s := pushed(patchA, patchA)
	for range 3 {
		if e := onlyPush(t, d, prev, s); rebaseOnly(e) != "true" || e.Trigger() != "rebase" || len(e.Commits) != 0 {
			t.Errorf("push = %+v, want a rebase", e)
		}
	}
	if got, want := f.requests(), []string{"metadata"}; !slices.Equal(got, want) {
		t.Errorf("asked %v, want %v", got, want)
	}
}

// When the newest version is not the one of the head (a push landed
// while fetching), nothing says whether the push changed anything: the
// version at hand is the one the cursor saw.
func TestRebaseOnlyUnknownWhenTheVersionIsNotTheHeads(t *testing.T) {
	for _, release := range []string{"18.5.1-ee", "17.11.4"} {
		f := &differFake{release: release, compare: map[string]string{
			"base1..head1": comparison(sameDiff),
			"head1..head3": comparison(otherDiff),
		}}
		s := snapshot()
		s.Versions[0] = gitlab.Version{ID: 1, HeadCommitSHA: "head1", BaseCommitSHA: "base1", CreatedAt: at(2), PatchIDSHA: patchA}
		prev := NewCursor(s)
		prev.SHA = "head1"
		s.MR.SHA, s.Unsettled = "head3", true

		e := onlyPush(t, &Differ{Client: f.client(t), People: People{Me: "me"}}, prev, s)
		if got := rebaseOnly(e); got != "unknown" || e.Trigger() != "push" {
			t.Errorf("GitLab %s: rebase_only = %s, trigger %s; want unknown, push", release, got, e.Trigger())
		}
		if got, want := f.requests(), []string{"commits head1..head3"}; !slices.Equal(got, want) {
			t.Errorf("GitLab %s: asked %v, want %v", release, got, want)
		}
	}
}

// What ends a wait by default: a push whose changes differ, and a push
// that could not be compared. Only a push known to keep the changes is a
// "rebase", which the default leaves out.
func TestTriggerOfAPush(t *testing.T) {
	yes, no := true, false
	for _, tt := range []struct {
		rebaseOnly *bool
		want       string
	}{{nil, "push"}, {&no, "push"}, {&yes, "rebase"}} {
		e := Event{Kind: "push", RebaseOnly: tt.rebaseOnly}
		if got := e.Trigger(); got != tt.want {
			t.Errorf("rebase_only %s: trigger = %s, want %s", rebaseOnly(e), got, tt.want)
		}
		if ends := slices.Contains(DefaultTriggers, e.Trigger()); ends != (tt.want == "push") {
			t.Errorf("rebase_only %s: ends a wait by default = %v", rebaseOnly(e), ends)
		}
	}
	// An absent rebase_only is absent from the JSON too, not false.
	b, _ := json.Marshal(Event{Kind: "push"})
	if strings.Contains(string(b), "rebase_only") {
		t.Errorf("unknown rebase_only is printed: %s", b)
	}
}
