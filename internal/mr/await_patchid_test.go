package mr

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/gitlab"
)

// GitLab computes the patch ID of a diff version just after it creates
// it. A wait that polls in between looks again at the next poll, twice
// at most, instead of leaving the decision to the comparison of the two
// diffs.
func TestAwaitsPatchID(t *testing.T) {
	ctx := context.Background()
	f := &differFake{release: "18.5.1-ee"}
	d := &Differ{Client: f.client(t), People: People{Me: "me"}}
	prev, s := pushed(patchA, "")
	for poll := 1; poll <= patchIDPolls; poll++ {
		if !d.AwaitsPatchID(ctx, prev, s) {
			t.Fatalf("poll %d: the push is not held back", poll)
		}
	}
	if d.AwaitsPatchID(ctx, prev, s) || d.AwaitsPatchID(ctx, prev, s) {
		t.Errorf("poll %d: the push is still held back", patchIDPolls+1)
	}
	if got, want := f.requests(), []string{"metadata"}; !slices.Equal(got, want) {
		t.Errorf("asked %v, want %v", got, want)
	}
	// GitLab never computed it: the diffs decide.
	f.compare = map[string]string{"base1..head1": comparison(sameDiff), "base2..head2": comparison(sameDiff)}
	if e := onlyPush(t, d, prev, s); rebaseOnly(e) != "true" {
		t.Errorf("push = %+v, want a rebase told from the diffs", e)
	}

	// Another push is given its two polls too.
	s.MR.SHA, s.Versions[0] = "head3", gitlab.Version{ID: 3, HeadCommitSHA: "head3", BaseCommitSHA: "base2", CreatedAt: at(9)}
	if !d.AwaitsPatchID(ctx, prev, s) {
		t.Error("another push is not held back")
	}
	// The patch ID is there at the next poll: it decides at once.
	s.Versions[0].PatchIDSHA = patchA
	if d.AwaitsPatchID(ctx, prev, s) {
		t.Error("a push with its patch ID is held back")
	}
	if e := onlyPush(t, d, prev, s); rebaseOnly(e) != "true" {
		t.Errorf("push = %+v, want a rebase told from the patch IDs", e)
	}
	if got := f.requests(); slices.Contains(got, "hash base2..head3") {
		t.Errorf("asked %v, want no comparison of the new diff", got)
	}
}

// Nothing is held back when the patch ID would not decide, or will not
// come.
func TestAwaitsPatchIDOnlyWhenItWouldDecide(t *testing.T) {
	tests := []struct {
		name    string
		release string
		edit    func(prev *Cursor, s *Snapshot)
		asked   []string
	}{
		{name: "GitLab before 18.2", release: "17.11.4", asked: []string{"metadata"}},
		{name: "a version that cannot be read", release: "main", asked: []string{"metadata"}},
		{name: "a cursor without a patch ID", release: "18.5.1-ee", edit: func(prev *Cursor, s *Snapshot) { prev.Patch = "" }},
		{name: "no push", release: "18.5.1-ee", edit: func(prev *Cursor, s *Snapshot) { prev.SHA = s.MR.SHA }},
		{name: "the version has its patch ID", release: "18.5.1-ee", edit: func(prev *Cursor, s *Snapshot) { s.Versions[0].PatchIDSHA = patchB }},
		{name: "a push is landing", release: "18.5.1-ee", edit: func(prev *Cursor, s *Snapshot) { s.Unsettled = true }},
		{name: "the version is not the head's", release: "18.5.1-ee", edit: func(prev *Cursor, s *Snapshot) { s.MR.SHA = "head3" }},
		{name: "no version", release: "18.5.1-ee", edit: func(prev *Cursor, s *Snapshot) { s.Versions = nil }},
		// GitLab computes no patch ID when the merge request has no change.
		{name: "the base is the head", release: "18.5.1-ee", edit: func(prev *Cursor, s *Snapshot) { s.Versions[0].BaseCommitSHA = "head2" }},
	}
	for _, tt := range tests {
		f := &differFake{release: tt.release}
		d := &Differ{Client: f.client(t), People: People{Me: "me"}}
		prev, s := pushed(patchA, "")
		if tt.edit != nil {
			tt.edit(&prev, s)
		}
		for range 2 {
			if d.AwaitsPatchID(context.Background(), prev, s) {
				t.Errorf("%s: the push is held back", tt.name)
			}
		}
		if got := f.requests(); !slices.Equal(got, tt.asked) {
			t.Errorf("%s: asked %v, want %v", tt.name, got, tt.asked)
		}
	}
}

// A GitLab that fails to say its version is asked again, but a wait may
// poll every 5 seconds: at most once a minute.
func TestVersionIsAskedAtMostOnceAMinute(t *testing.T) {
	clock := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	f := &differFake{releaseStatus: 500, release: "18.5.1-ee", compare: map[string]string{
		"base1..head1": comparison(sameDiff),
		"base2..head2": comparison(otherDiff),
	}}
	d := &Differ{Client: f.client(t), People: People{Me: "me"}, now: func() time.Time { return clock }}
	prev, s := pushed(patchA, patchA)
	poll := func(want string) {
		t.Helper()
		if e := onlyPush(t, d, prev, s); rebaseOnly(e) != want {
			t.Errorf("at %s: rebase_only = %s, want %s", clock.Format("15:04:05"), rebaseOnly(e), want)
		}
	}
	metadata := func() int {
		n := 0
		for _, r := range f.requests() {
			if r == "metadata" {
				n++
			}
		}
		return n
	}
	// Not trusted: the diffs decide, and they differ.
	for range 5 {
		poll("false")
		clock = clock.Add(5 * time.Second)
	}
	if got := metadata(); got != 1 {
		t.Errorf("five polls in 25 seconds: the version was asked %d times, want 1", got)
	}
	clock = clock.Add(30 * time.Second)
	poll("false")
	if got := metadata(); got != 0 {
		t.Errorf("after 55 seconds: the version was asked %d times, want 0", got)
	}
	clock = clock.Add(5 * time.Second)
	poll("false")
	poll("false")
	if got := metadata(); got != 1 {
		t.Errorf("after a minute: the version was asked %d times, want 1", got)
	}

	// GitLab answers again: asked once the minute has passed, and
	// trusted from then on.
	f.mu.Lock()
	f.releaseStatus = 0
	f.mu.Unlock()
	poll("false")
	clock = clock.Add(time.Minute)
	for range 3 {
		poll("true")
		clock = clock.Add(5 * time.Second)
	}
	if got := metadata(); got != 1 {
		t.Errorf("GitLab back: the version was asked %d times, want 1", got)
	}
}
