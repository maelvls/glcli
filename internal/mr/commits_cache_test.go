package mr

import (
	"context"
	"slices"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// A wait meets a push that does not end it (my own push, a kind left out
// of --on) at every poll: its commits are asked once, like its diffs.
func TestNewCommitsAreAskedOnce(t *testing.T) {
	tests := []struct {
		name    string
		release string
		after   string
		want    []string
	}{
		{"GitLab's patch IDs", "18.5.1-ee", patchB, []string{"metadata", "commits base2..head2"}},
		{"the compare API", "17.11.4", patchA, []string{"metadata", "hash base1..head1", "hash base2..head2", "commits base2..head2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &differFake{release: tt.release, compare: map[string]string{
				"base1..head1": comparison(sameDiff),
				"base2..head2": comparison(otherDiff),
			}}
			d := &Differ{Client: f.client(t), People: People{Me: "me"}}
			prev, s := pushed(patchA, tt.after)
			for range 3 {
				e := onlyPush(t, d, prev, s)
				if rebaseOnly(e) != "false" || len(e.Commits) != 1 || e.Commits[0].SHA != "c1" || e.Commits[0].Title != "a commit" {
					t.Errorf("push = %+v, want a change with its commit", e)
				}
			}
			if got := f.requests(); !slices.Equal(got, tt.want) {
				t.Errorf("asked %v, want %v", got, tt.want)
			}

			// Another push is another question.
			s.MR.SHA, s.Versions[0] = "head3", gitlab.Version{ID: 3, HeadCommitSHA: "head3", BaseCommitSHA: "base2", CreatedAt: at(9), PatchIDSHA: patchB}
			f.compare["base2..head3"] = comparison(otherDiff)
			for range 2 {
				onlyPush(t, d, prev, s)
			}
			if got := f.requests(); !slices.Contains(got, "commits base2..head3") || len(got) > 2 {
				t.Errorf("after another push: asked %v, want its commits once", got)
			}
		})
	}
}

// The commits of a push are not worth failing for: when GitLab fails to
// list them they are left out, and asked again at the next poll.
func TestNewCommitsAreAskedAgainAfterAFailure(t *testing.T) {
	f := &differFake{release: "18.5.1-ee", compareStatus: 500, compare: map[string]string{"base2..head2": comparison(otherDiff)}}
	d := &Differ{Client: f.client(t), People: People{Me: "me"}}
	prev, s := pushed(patchA, patchB)
	for range 2 {
		if e := onlyPush(t, d, prev, s); rebaseOnly(e) != "false" || len(e.Commits) != 0 {
			t.Errorf("GitLab failing: push = %+v, want a change without commits", e)
		}
	}
	f.mu.Lock()
	f.compareStatus = 0
	f.mu.Unlock()
	for range 2 {
		if e := onlyPush(t, d, prev, s); len(e.Commits) != 1 {
			t.Errorf("GitLab back: push = %+v, want its commit", e)
		}
	}
	want := []string{"metadata", "commits base2..head2", "commits base2..head2", "commits base2..head2"}
	if got := f.requests(); !slices.Equal(got, want) {
		t.Errorf("asked %v, want %v", got, want)
	}
}

// Commits that are gone stay gone: neither their diff nor their list is
// asked again at every poll.
func TestGoneCommitsAreAskedOnce(t *testing.T) {
	f := &differFake{release: "17.11.4"}
	d := &Differ{Client: f.client(t), People: People{Me: "me"}}
	prev, s := pushed("", "")
	for range 3 {
		if e := onlyPush(t, d, prev, s); rebaseOnly(e) != "unknown" || len(e.Commits) != 0 {
			t.Errorf("push = %+v, want unknown and no commits", e)
		}
	}
	want := []string{"hash base1..head1", "hash base2..head2", "commits base2..head2"}
	if got := f.requests(); !slices.Equal(got, want) {
		t.Errorf("asked %v, want %v", got, want)
	}

	// While GitLab fails, the diffs are not known and nothing is kept:
	// the error goes to the caller, who polls again.
	f = &differFake{release: "17.11.4", compareStatus: 500}
	d = &Differ{Client: f.client(t), People: People{Me: "me"}}
	if _, err := d.Events(context.Background(), prev, s); !gitlab.IsStatus(err, 500) {
		t.Errorf("GitLab failing: err = %v, want the 500", err)
	}
	f.mu.Lock()
	f.compareStatus, f.compare = 0, map[string]string{"base1..head1": comparison(sameDiff), "base2..head2": comparison(sameDiff)}
	f.mu.Unlock()
	if e := onlyPush(t, d, prev, s); rebaseOnly(e) != "true" {
		t.Errorf("GitLab back: push = %+v, want a rebase", e)
	}
}
