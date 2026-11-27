package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
)

// pollMR is one merge request of pollGitLab.
type pollMR struct {
	author    string
	updatedAt time.Time
	// versions are newest first; the head of the merge request is the head
	// of the first one unless sha is set.
	versions   []gitlab.Version
	sha        string
	notes      []gitlab.Note
	approvedBy []string
	pipeline   *gitlab.PipelineRef
	// gone leaves it out of the lists: merged, or no longer mine to review.
	gone bool
}

// pollGitLab is a GitLab whose merge requests change between the polls
// of a test, and that counts what is asked of it.
type pollGitLab struct {
	mu  sync.Mutex
	mrs map[int]*pollMR
	// fail answers this status to the paths ending with the key, such as
	// "/merge_requests/2/discussions".
	fail   map[string]int
	counts map[string]int
}

var mrPath = regexp.MustCompile(`/merge_requests/(\d+)(?:/(\w+))?$`)

func (f *pollGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.counts == nil {
		f.counts = map[string]int{}
	}
	mergeRequest := func(iid int, x *pollMR) gitlab.MergeRequest {
		m := gitlab.MergeRequest{
			ID: 100 + iid, IID: iid, ProjectID: 1, State: "opened", Author: gitlab.User{Username: x.author},
			SHA: x.sha, WebURL: mrsURL + strconv.Itoa(iid), CreatedAt: at(0), UpdatedAt: x.updatedAt, HeadPipeline: x.pipeline,
		}
		if m.SHA == "" {
			m.SHA = x.versions[0].HeadCommitSHA
		}
		m.References.Full = fmt.Sprintf("g/p!%d", iid)
		return m
	}
	for suffix, status := range f.fail {
		if len(r.URL.Path) >= len(suffix) && r.URL.Path[len(r.URL.Path)-len(suffix):] == suffix {
			f.counts["failed"]++
			http.Error(w, `{"message":"failed"}`, status)
			return
		}
	}
	var out any
	switch path := r.URL.Path; {
	case path == "/api/v4/merge_requests":
		f.counts["list"]++
		list := []gitlab.MergeRequest{}
		q := r.URL.Query()
		for iid := 1; iid <= len(f.mrs); iid++ {
			x := f.mrs[iid]
			if !x.gone && (q.Get("author_username") == x.author || q.Get("reviewer_username") != "" && x.author != "me") {
				m := mergeRequest(iid, x)
				m.HeadPipeline = nil // the lists do not have it
				list = append(list, m)
			}
		}
		out = list
	case path == "/api/v4/todos":
		f.counts["todos"]++
		out = []gitlab.Todo{}
	case path == "/api/v4/metadata":
		f.counts["metadata"]++
		out = map[string]string{"version": "18.5.1-ee"}
	case mrPath.MatchString(path):
		sub := mrPath.FindStringSubmatch(path)
		iid, _ := strconv.Atoi(sub[1])
		x := f.mrs[iid]
		switch sub[2] {
		case "":
			f.counts["mr"]++
			out = mergeRequest(iid, x)
		case "approvals":
			f.counts["approvals"]++
			var a gitlab.Approvals
			for _, u := range x.approvedBy {
				a.ApprovedBy = append(a.ApprovedBy, approvedBy(u).ApprovedBy...)
			}
			out = a
		case "discussions":
			f.counts["discussions"]++
			ds := []gitlab.Discussion{}
			for _, n := range x.notes {
				ds = append(ds, gitlab.Discussion{ID: "d", Notes: []gitlab.Note{n}})
			}
			out = ds
		case "versions":
			f.counts["versions"]++
			out = x.versions
		}
	}
	if out == nil {
		f.counts["other"]++
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

// change edits the merge requests between two polls.
func (f *pollGitLab) change(edit func(mrs map[int]*pollMR)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit(f.mrs)
}

func (f *pollGitLab) failing(fail map[string]int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

// polled returns what was asked since the last call: the total, and the
// count of each kind of request.
func (f *pollGitLab) polled() (total int, counts map[string]int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	counts, f.counts = f.counts, nil
	for _, n := range counts {
		total += n
	}
	return total, counts
}

func (f *pollGitLab) client(t *testing.T) *gitlab.Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := gitlab.New(config.Host{Name: fmt.Sprintf("poll-%d", hosts.Add(1)), Token: "x"})
	c.BaseURL = srv.URL
	return c
}

// clock is the time of the process that polls.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock { return &clock{t: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)} }

// poller calls Collect as inbox wait does, with one cache.
type poller struct {
	t     *testing.T
	f     *pollGitLab
	c     *gitlab.Client
	o     Options
	clock *clock
}

func newPoller(t *testing.T, f *pollGitLab, roles Roles, cached bool) *poller {
	p := &poller{t: t, f: f, c: f.client(t), clock: newClock()}
	p.o = Options{Host: p.c.Host.Name, People: me, Roles: roles}
	if cached {
		p.o.Cache = NewCache(p.clock.now)
	}
	return p
}

// poll waits for d, collects, and returns the result with the requests
// it took.
func (p *poller) poll(d time.Duration) (res *Result, total int, counts map[string]int) {
	p.t.Helper()
	p.clock.advance(d)
	res, err := Collect(context.Background(), p.c, p.o)
	if err != nil {
		p.t.Fatalf("Collect: %v", err)
	}
	total, counts = p.f.polled()
	if counts["other"] != 0 {
		p.t.Errorf("unexpected requests: %v", counts)
	}
	return res, total, counts
}

// item returns the item of the merge request iid, or fails.
func item(t *testing.T, res *Result, iid int) Item {
	t.Helper()
	for _, it := range res.Items {
		if it.Ref == fmt.Sprintf("g/p!%d", iid) {
			return it
		}
	}
	t.Fatalf("no item for !%d in %+v", iid, res.Items)
	return Item{}
}

// reviewed is a merge request of bob that I commented at :03: nothing
// waits on me.
func reviewed() *pollMR {
	return &pollMR{author: "bob", updatedAt: at(3), versions: []gitlab.Version{version(2, "A")}, notes: []gitlab.Note{note(1, "me", 3, "nit", false)}}
}

const interval = 2 * time.Minute

// The requests of each poll of inbox wait, without and with the cache:
// eight merge requests that do not change, then one that does.
func TestCacheRequestsPerPoll(t *testing.T) {
	const n = 8
	gitLab := func() *pollGitLab {
		f := &pollGitLab{mrs: map[int]*pollMR{}}
		for iid := 1; iid <= n; iid++ {
			f.mrs[iid] = reviewed()
		}
		return f
	}
	push := func(mrs map[int]*pollMR) {
		x := mrs[5]
		x.versions = append([]gitlab.Version{version(30, "B")}, x.versions...)
		x.notes = append(x.notes, pushBy(2, "bob", 30))
		x.updatedAt = at(30)
	}
	full := 1 + 4*n // the list, then four requests for each merge request

	// Before: everything at each poll.
	p := newPoller(t, gitLab(), Roles{Review: true}, false)
	for i := 1; i <= 3; i++ {
		if _, total, counts := p.poll(interval); total != full {
			t.Errorf("without the cache, poll %d: %d requests, want %d: %v", i, total, full, counts)
		}
	}
	p.f.change(push)
	if res, total, counts := p.poll(interval); total != full || !slices.Equal(item(t, res, 5).Reasons, []string{"new_commits"}) {
		t.Errorf("without the cache, after the push: %d requests, want %d: %v, reasons = %v", total, full, counts, item(t, res, 5).Reasons)
	}

	// After.
	p = newPoller(t, gitLab(), Roles{Review: true}, true)
	steps := []struct {
		name   string
		change func(map[int]*pollMR)
		want   int
		// reasons are those of the merge request 5.
		reasons []string
	}{
		{name: "first poll: everything", want: full},
		{name: "second poll: everything again, read long enough after the last change", want: full},
		{name: "third poll: the list", want: 1},
		{name: "fourth poll: the list", want: 1},
		{name: "one merge request changed: the list and it", change: push, want: 1 + 4, reasons: []string{"new_commits"}},
		{name: "the poll after the change: it again", want: 1 + 4, reasons: []string{"new_commits"}},
		{name: "then the list", want: 1, reasons: []string{"new_commits"}},
		{name: "and the list", want: 1, reasons: []string{"new_commits"}},
	}
	for _, step := range steps {
		if step.change != nil {
			p.f.change(step.change)
		}
		res, total, counts := p.poll(interval)
		t.Logf("with the cache, %s: %d requests %v", step.name, total, counts)
		if total != step.want {
			t.Errorf("with the cache, %s: %d requests, want %d: %v", step.name, total, step.want, counts)
		}
		if len(res.Items) != n || len(res.Warnings) != 0 {
			t.Errorf("with the cache, %s: %d items, warnings %v, want %d items", step.name, len(res.Items), res.Warnings, n)
		}
		if got := item(t, res, 5); !slices.Equal(got.Reasons, step.reasons) || step.reasons != nil && !got.At.Equal(at(30)) {
			t.Errorf("with the cache, %s: reasons = %v at %v, want %v", step.name, got.Reasons, got.At, step.reasons)
		}
		for iid := 1; iid <= n; iid++ {
			if got := item(t, res, iid); iid != 5 && got.Waiting() {
				t.Errorf("with the cache, %s: !%d has reasons %v", step.name, iid, got.Reasons)
			}
		}
	}
}

// What changes without updated_at is still read at each poll: the
// pipeline and the merge status of my own merge requests, and the
// approvals of those I approved.
func TestCacheReadsWhatUpdatedAtDoesNotCover(t *testing.T) {
	f := &pollGitLab{mrs: map[int]*pollMR{
		// Mine, its pipeline running.
		1: {author: "me", updatedAt: at(3), versions: []gitlab.Version{version(2, "A")},
			pipeline: &gitlab.PipelineRef{ID: 7, Status: "running", SHA: "head2", UpdatedAt: at(2)}},
		// Approved by me at :03, rebased at :05: my approval stayed.
		2: {author: "bob", updatedAt: at(5), versions: []gitlab.Version{version(5, "A"), version(2, "A")},
			notes: []gitlab.Note{note(1, "me", 3, approvedNote, true)}, approvedBy: []string{"me", "alice"}},
		3: reviewed(),
	}}
	p := newPoller(t, f, Roles{Review: true, Authored: true}, true)
	p.poll(interval)
	p.poll(interval)

	// Settled: the two lists, my merge request, and the approvals of the
	// one I approved.
	steady := map[string]int{"list": 2, "mr": 1, "approvals": 1}
	check := func(name string, counts, want map[string]int) {
		t.Helper()
		if fmt.Sprint(counts) != fmt.Sprint(want) {
			t.Errorf("%s: requests %v, want %v", name, counts, want)
		}
	}
	res, _, counts := p.poll(interval)
	check("nothing changed", counts, steady)
	if item(t, res, 1).Waiting() || item(t, res, 2).Waiting() || item(t, res, 3).Waiting() {
		t.Errorf("nothing changed: items = %+v", res.Items)
	}

	// The pipeline fails: GitLab does not touch the merge request.
	f.change(func(mrs map[int]*pollMR) {
		mrs[1].pipeline = &gitlab.PipelineRef{ID: 7, Status: "failed", SHA: "head2", UpdatedAt: at(40)}
	})
	res, _, counts = p.poll(interval)
	check("pipeline failed", counts, steady)
	if got := item(t, res, 1); !slices.Equal(got.Reasons, []string{"pipeline_failed"}) || !got.At.Equal(at(40)) || got.Pipeline != "failed" {
		t.Errorf("pipeline failed: reasons = %v at %v, pipeline %q", got.Reasons, got.At, got.Pipeline)
	}

	// GitLab removes my approval and says nothing: everything is read
	// again, and the rebase that kept the patch now waits on me.
	f.change(func(mrs map[int]*pollMR) { mrs[2].approvedBy = []string{"alice"} })
	res, _, counts = p.poll(interval)
	check("my approval removed", counts, map[string]int{"list": 2, "mr": 2, "approvals": 2, "discussions": 1, "versions": 1, "metadata": 1})
	if got := item(t, res, 2); !slices.Equal(got.Reasons, []string{"new_commits"}) || got.ApprovedByMe {
		t.Errorf("my approval removed: reasons = %v, approved by me: %v", got.Reasons, got.ApprovedByMe)
	}
	// No longer among the approvers: its approvals are not read any more.
	_, _, counts = p.poll(interval)
	check("after my approval was removed", counts, map[string]int{"list": 2, "mr": 1})

	// The approval of someone else goes, on a merge request I did not
	// approve: not read, and not a reason.
	f.change(func(mrs map[int]*pollMR) { mrs[2].approvedBy = nil })
	_, _, counts = p.poll(interval)
	check("the approval of someone else removed", counts, map[string]int{"list": 2, "mr": 1})

	// A push on my merge request that updated_at does not show (it cannot
	// be told from the notes kept): everything is read again.
	f.change(func(mrs map[int]*pollMR) {
		mrs[1].versions = append([]gitlab.Version{version(45, "B")}, mrs[1].versions...)
	})
	_, _, counts = p.poll(interval)
	check("another head", counts, map[string]int{"list": 2, "mr": 2, "approvals": 1, "discussions": 1, "versions": 1})
	_, _, counts = p.poll(interval)
	check("after another head", counts, map[string]int{"list": 2, "mr": 1})
}

// A merge request changed between the list and its own read: the
// updated_at of the two differ, and everything is read.
func TestCacheReadsAgainWhenTheMergeRequestMovedSinceTheList(t *testing.T) {
	f := &pollGitLab{mrs: map[int]*pollMR{1: {author: "me", updatedAt: at(3), versions: []gitlab.Version{version(2, "A")}}}}
	p := newPoller(t, f, Roles{Authored: true}, true)
	p.poll(interval)
	p.poll(interval)
	if _, total, counts := p.poll(interval); total != 2 {
		t.Fatalf("settled: %d requests, want the list and the merge request: %v", total, counts)
	}
	// The cache keeps the updated_at of the list: the entry it has is for
	// :03, the list now says :03 too, but the merge request says :50.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/merge_requests" {
			f.ServeHTTP(w, r)
			f.change(func(mrs map[int]*pollMR) {
				mrs[1].updatedAt = at(50)
				mrs[1].notes = []gitlab.Note{note(1, "alice", 50, "looks wrong", false)}
			})
			return
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	p.c.BaseURL = srv.URL
	res, _, counts := p.poll(interval)
	if fmt.Sprint(counts) != fmt.Sprint(map[string]int{"list": 1, "mr": 2, "approvals": 1, "discussions": 1, "versions": 1}) {
		t.Errorf("moved since the list: requests %v", counts)
	}
	if got := item(t, res, 1); !slices.Equal(got.Reasons, []string{"new_comments"}) {
		t.Errorf("moved since the list: reasons = %v, want new_comments", got.Reasons)
	}
}

// GitLab does not change updated_at for a note written within a minute
// of the last change (ThrottledTouch): nothing is kept of a merge request
// until it was read 90 seconds after its updated_at was first seen.
func TestCacheKeepsNothingUntilSettled(t *testing.T) {
	f := &pollGitLab{mrs: map[int]*pollMR{1: reviewed()}}
	p := newPoller(t, f, Roles{Review: true}, true)
	const short = 30 * time.Second
	reply := func(id, min int) func(map[int]*pollMR) {
		return func(mrs map[int]*pollMR) {
			mrs[1].notes = append(mrs[1].notes, note(id, "alice", min, "@me done", false))
		}
	}

	// Seen at 0 s; read at 0, 30 and 60 s: too early to be kept.
	for i, name := range []string{"at 0 s", "at 30 s", "at 60 s"} {
		if i == 1 {
			// A reply right after the first poll, that updated_at does not show.
			f.change(reply(2, 10))
		}
		res, total, counts := p.poll(short)
		if total != 5 {
			t.Errorf("%s: %d requests, want 5: %v", name, total, counts)
		}
		if got, want := item(t, res, 1).Waiting(), i >= 1; got != want {
			t.Errorf("%s: waiting = %v, want %v", name, got, want)
		}
	}
	// Read at 90 s: kept from now on.
	if _, total, counts := p.poll(short); total != 5 {
		t.Errorf("at 90 s: %d requests, want 5: %v", total, counts)
	}
	for _, name := range []string{"at 120 s", "at 150 s"} {
		if _, total, counts := p.poll(short); total != 1 {
			t.Errorf("%s: %d requests, want 1: %v", name, total, counts)
		}
	}

	// What the reasoning missed costs a delay, not a miss: a reply that
	// GitLab would not have shown in updated_at is seen once the merge
	// request is 20 minutes old in the cache.
	f.change(reply(3, 20))
	res, total, _ := p.poll(short)
	if total != 1 || !item(t, res, 1).At.Equal(at(10)) {
		t.Errorf("a change that updated_at does not show: %d requests, at %v, want 1 request and the reply of :10", total, item(t, res, 1).At)
	}
	// Read at 90 s, now at 180 s: 20 minutes after the read is 1290 s.
	res, total, _ = p.poll(maxAge - 90*time.Second - time.Second)
	if total != 1 || !item(t, res, 1).At.Equal(at(10)) {
		t.Errorf("one second before 20 minutes: %d requests, at %v, want 1 request and the reply of :10", total, item(t, res, 1).At)
	}
	res, total, counts := p.poll(time.Second)
	if total != 5 || !item(t, res, 1).At.Equal(at(20)) {
		t.Errorf("after 20 minutes: %d requests %v, at %v, want 5 requests and the reply of :20", total, counts, item(t, res, 1).At)
	}
	// And kept again: it was read long after its updated_at was first seen.
	if _, total, counts := p.poll(short); total != 1 {
		t.Errorf("after the 20 minutes read: %d requests, want 1: %v", total, counts)
	}
}

// A merge request that could not be read is warned about at that poll
// only, and read again in full at the next one: errors are not kept.
func TestCacheDoesNotKeepErrors(t *testing.T) {
	f := &pollGitLab{mrs: map[int]*pollMR{
		1: reviewed(),
		2: {author: "me", updatedAt: at(3), versions: []gitlab.Version{version(2, "A")}},
	}}
	p := newPoller(t, f, Roles{Review: true, Authored: true}, true)
	warned := func(name string, res *Result, iid int) {
		t.Helper()
		if len(res.Warnings) != 1 || len(res.Items) != 1 || res.Items[0].Ref == fmt.Sprintf("g/p!%d", iid) {
			t.Errorf("%s: warnings = %v, %d items, want one warning about !%d and the other item", name, res.Warnings, len(res.Items), iid)
		}
	}
	fine := func(name string, res *Result, counts, want map[string]int) {
		t.Helper()
		if len(res.Warnings) != 0 || len(res.Items) != 2 || fmt.Sprint(counts) != fmt.Sprint(want) {
			t.Errorf("%s: warnings = %v, %d items, requests %v, want no warning, 2 items and %v", name, res.Warnings, len(res.Items), counts, want)
		}
	}
	everything := map[string]int{"list": 2, "mr": 2, "approvals": 2, "discussions": 2, "versions": 2}

	// The first read fails.
	f.failing(map[string]int{"/merge_requests/1/discussions": 500})
	res, _, _ := p.poll(interval)
	warned("first read failing", res, 1)
	f.failing(nil)
	res, _, counts := p.poll(interval)
	fine("after the first read failed", res, counts, everything)
	// !2 was read at the first poll, too early; !1 was not read at all.
	res, _, counts = p.poll(interval)
	fine("one poll later", res, counts, map[string]int{"list": 2, "mr": 1})

	// The one request of a kept merge request fails.
	f.failing(map[string]int{"/merge_requests/2": 502})
	res, _, counts = p.poll(interval)
	warned("kept, its own read failing", res, 2)
	if fmt.Sprint(counts) != fmt.Sprint(map[string]int{"list": 2, "failed": 1}) {
		t.Errorf("kept, its own read failing: requests %v", counts)
	}
	// Still failing: warned again, as without the cache, with a full read
	// attempted.
	res, _, _ = p.poll(interval)
	warned("still failing", res, 2)
	f.failing(nil)
	res, _, counts = p.poll(interval)
	fine("answering again", res, counts, map[string]int{"list": 2, "mr": 1, "approvals": 1, "discussions": 1, "versions": 1})
	res, _, counts = p.poll(interval)
	fine("and kept again", res, counts, map[string]int{"list": 2, "mr": 1})
}

// The merge requests that left the lists are forgotten, and read in full
// if they come back.
func TestCacheForgetsWhatLeftTheLists(t *testing.T) {
	f := &pollGitLab{mrs: map[int]*pollMR{1: reviewed(), 2: reviewed()}}
	p := newPoller(t, f, Roles{Review: true}, true)
	entries := func() int {
		p.o.Cache.mu.Lock()
		defer p.o.Cache.mu.Unlock()
		return len(p.o.Cache.entries)
	}
	p.poll(interval)
	p.poll(interval)
	if _, total, _ := p.poll(interval); total != 1 || entries() != 2 {
		t.Fatalf("settled: %d requests, %d entries, want 1 and 2", total, entries())
	}
	f.change(func(mrs map[int]*pollMR) { mrs[2].gone = true })
	if res, total, _ := p.poll(interval); total != 1 || entries() != 1 || len(res.Items) != 1 {
		t.Errorf("one gone: %d requests, %d entries, %d items, want 1, 1 and 1", total, entries(), len(res.Items))
	}
	f.change(func(mrs map[int]*pollMR) { mrs[2].gone = false })
	if res, total, counts := p.poll(interval); total != 5 || entries() != 2 || len(res.Items) != 2 {
		t.Errorf("back: %d requests %v, %d entries, %d items, want 5, 2 and 2", total, counts, entries(), len(res.Items))
	}

	// Another host has its own merge requests.
	other := newPoller(t, &pollGitLab{mrs: map[int]*pollMR{1: reviewed()}}, Roles{Review: true}, false)
	other.o.Cache, other.clock = p.o.Cache, p.clock
	other.poll(interval)
	if entries() != 3 {
		t.Errorf("two hosts: %d entries, want 3", entries())
	}
	p.poll(interval)
	if entries() != 3 {
		t.Errorf("two hosts, after a poll of the first: %d entries, want 3", entries())
	}
}

// What cannot be trusted is not kept: a read during which a push landed,
// and a merge request without updated_at.
func TestCacheKeepsOnlyWhatItCanTrust(t *testing.T) {
	f := &pollGitLab{mrs: map[int]*pollMR{
		1: {author: "bob", updatedAt: at(3), versions: []gitlab.Version{version(2, "A")}, sha: "another"},
		2: {author: "bob", versions: []gitlab.Version{version(2, "A")}},
	}}
	p := newPoller(t, f, Roles{Review: true}, true)
	for i := 1; i <= 4; i++ {
		// Fetch reads a merge request three times when its head and its
		// newest version disagree.
		if _, _, counts := p.poll(interval); counts["discussions"] != 3+1 {
			t.Errorf("poll %d: discussions read %d times, want 4: %v", i, counts["discussions"], counts)
		}
	}
}
