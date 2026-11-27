package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
)

// What is kept is read again in full once it is 20 minutes old: the
// value is part of what inbox wait promises (its help says it), so it is
// written here, not taken from the code.
func TestCacheReadsEverythingAfterTwentyMinutes(t *testing.T) {
	f := &pollGitLab{mrs: map[int]*pollMR{1: reviewed()}}
	p := newPoller(t, f, Roles{Review: true}, true)
	p.poll(0)
	// Read at 2 minutes, long enough after the first poll: kept from now.
	if _, total, _ := p.poll(2 * time.Minute); total != 5 {
		t.Fatalf("second poll: %d requests, want 5", total)
	}
	if _, total, _ := p.poll(19*time.Minute + 59*time.Second); total != 1 {
		t.Errorf("19 min 59 s after the read: %d requests, want 1", total)
	}
	if _, total, _ := p.poll(time.Second); total != 5 {
		t.Errorf("20 min after the read: %d requests, want 5", total)
	}
	// Nothing is kept until 90 seconds after the updated_at was first
	// seen, 89 seconds are not enough.
	g := &pollGitLab{mrs: map[int]*pollMR{1: reviewed()}}
	p = newPoller(t, g, Roles{Review: true}, true)
	p.poll(0)
	p.poll(89 * time.Second)
	if _, total, _ := p.poll(time.Minute); total != 5 {
		t.Errorf("read 89 s after the first poll: %d requests at the next poll, want 5", total)
	}
	if _, total, _ := p.poll(time.Minute); total != 1 {
		t.Errorf("read 149 s after the first poll: %d requests at the next poll, want 1", total)
	}
}

// Two merge requests with the same number in two projects are two merge
// requests.
func TestCacheTellsProjectsApart(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	mergeRequest := func(projectID int, path string) gitlab.MergeRequest {
		m := gitlab.MergeRequest{
			ID: 100 + projectID, IID: 1, ProjectID: projectID, State: "opened", Author: gitlab.User{Username: "bob"}, SHA: "head2",
			WebURL: "https://gitlab.example/" + path + "/-/merge_requests/1", CreatedAt: at(0), UpdatedAt: at(3),
		}
		m.References.Full = path + "!1"
		return m
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		// g/p!1 has a comment of mine, g/q!1 was never reviewed.
		projectID, path := 1, "g/p"
		if strings.Contains(r.URL.Path, "/projects/g/q/") {
			projectID, path = 2, "g/q"
		}
		var out any
		switch p := r.URL.Path; {
		case p == "/api/v4/merge_requests":
			out = []gitlab.MergeRequest{mergeRequest(1, "g/p"), mergeRequest(2, "g/q")}
		case strings.HasSuffix(p, "/merge_requests/1"):
			out = mergeRequest(projectID, path)
		case strings.HasSuffix(p, "/approvals"):
			out = gitlab.Approvals{}
		case strings.HasSuffix(p, "/versions"):
			out = []gitlab.Version{version(2, "A")}
		case strings.HasSuffix(p, "/discussions"):
			ds := []gitlab.Discussion{}
			if projectID == 1 {
				ds = append(ds, gitlab.Discussion{ID: "d", Notes: []gitlab.Note{note(1, "me", 3, "nit", false)}})
			}
			out = ds
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	c := gitlab.New(config.Host{Name: fmt.Sprintf("projects-%d", hosts.Add(1)), Token: "x"})
	c.BaseURL = srv.URL

	clock := newClock()
	o := Options{Host: c.Host.Name, People: me, Roles: Roles{Review: true}, Cache: NewCache(clock.now)}
	for poll, want := range []int{9, 9, 1, 1} {
		clock.advance(interval)
		res, err := Collect(context.Background(), c, o)
		if err != nil || len(res.Warnings) != 0 {
			t.Fatalf("poll %d: res = %+v, err = %v", poll+1, res, err)
		}
		mu.Lock()
		got := requests
		requests = 0
		mu.Unlock()
		if got != want {
			t.Errorf("poll %d: %d requests, want %d", poll+1, got, want)
		}
		if p, q := item(t, res, 1), itemOf(t, res, "g/q!1"); p.Waiting() || !slices.Equal(q.Reasons, []string{"review_requested"}) {
			t.Errorf("poll %d: g/p!1 %v, g/q!1 %v, want none and review_requested", poll+1, p.Reasons, q.Reasons)
		}
	}
}

func itemOf(t *testing.T, res *Result, ref string) Item {
	t.Helper()
	for _, it := range res.Items {
		if it.Ref == ref {
			return it
		}
	}
	t.Fatalf("no item for %s in %+v", ref, res.Items)
	return Item{}
}

// The approvals of a merge request I approved cannot be read at a poll:
// warned about at that poll only, and everything is read at the next.
func TestCacheApprovalsThatCannotBeRead(t *testing.T) {
	f := &pollGitLab{mrs: map[int]*pollMR{
		1: {author: "bob", updatedAt: at(3), versions: []gitlab.Version{version(2, "A")},
			notes: []gitlab.Note{note(1, "me", 3, approvedNote, true)}, approvedBy: []string{"me"}},
		2: reviewed(),
	}}
	p := newPoller(t, f, Roles{Review: true}, true)
	p.poll(interval)
	p.poll(interval)
	want := func(name string, res *Result, counts, wantCounts map[string]int, warnings, items int) {
		t.Helper()
		if len(res.Warnings) != warnings || len(res.Items) != items || fmt.Sprint(counts) != fmt.Sprint(wantCounts) {
			t.Errorf("%s: warnings = %v, %d items, requests %v, want %d warnings, %d items and %v", name, res.Warnings, len(res.Items), counts, warnings, items, wantCounts)
		}
	}
	res, _, counts := p.poll(interval)
	want("kept", res, counts, map[string]int{"list": 1, "approvals": 1}, 0, 2)

	f.failing(map[string]int{"/merge_requests/1/approvals": 500})
	res, _, counts = p.poll(interval)
	want("approvals failing", res, counts, map[string]int{"list": 1, "failed": 1}, 1, 1)
	if len(res.Warnings) == 1 && !strings.Contains(res.Warnings[0], "merge_requests/1") {
		t.Errorf("approvals failing: warning %q, want one about !1", res.Warnings[0])
	}

	f.failing(nil)
	res, _, counts = p.poll(interval)
	want("answering again", res, counts, map[string]int{"list": 1, "mr": 1, "approvals": 1, "discussions": 1, "versions": 1}, 0, 2)
	res, _, counts = p.poll(interval)
	want("kept again", res, counts, map[string]int{"list": 1, "approvals": 1}, 0, 2)
}

// Expire makes one Collect read everything, and Kept says whether the
// last one did not: inbox wait reads everything before it prints.
func TestCacheExpire(t *testing.T) {
	f := &pollGitLab{mrs: map[int]*pollMR{1: reviewed(), 2: reviewed()}}
	p := newPoller(t, f, Roles{Review: true}, true)
	for i, kept := range []bool{false, false, true} {
		if _, _, _ = p.poll(interval); p.o.Cache.Kept() != kept {
			t.Errorf("poll %d: kept = %v, want %v", i+1, !kept, kept)
		}
	}
	p.o.Cache.Expire()
	if _, total, counts := p.poll(interval); total != 9 || p.o.Cache.Kept() {
		t.Errorf("after Expire: %d requests %v, kept = %v, want 9 and false", total, counts, p.o.Cache.Kept())
	}
	// What that Collect read is kept at once: the updated_at were first
	// seen long ago.
	if _, total, counts := p.poll(interval); total != 1 || !p.o.Cache.Kept() {
		t.Errorf("the poll after Expire: %d requests %v, kept = %v, want 1 and true", total, counts, p.o.Cache.Kept())
	}
}
