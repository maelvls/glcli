package mr

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/gitlab"
)

// amended is snapshot() after a push from head1 to head2 on the same
// base (an amend, a reword, a commit on top), and the cursor taken
// before it, with the patch IDs GitLab gave for each head.
func amended(before, after string) (Cursor, *Snapshot) {
	prev, s := pushed(before, after)
	prev.Base = s.Versions[0].BaseCommitSHA
	return prev, s
}

const (
	sameTrees  = `{"commits": [], "diffs": [], "compare_timeout": false}`
	otherTrees = `{"commits": [], "diffs": [{"old_path": "values.yaml", "new_path": "values.yaml", "a_mode": "100644", "b_mode": "100644", "diff": "@@ -3,7 +3,7 @@ staging:\n..."}]}`
)

// A patch ID does not cover the hunk headers: a change moved to another
// place of its file where the lines around it are the same keeps it
// (timeout: 30 to 60, under staging: then under production:). When the
// base did not move, the two heads are compared with each other, on any
// GitLab: on one base, the same changes are the same tree.
func TestSameBaseComparesTheHeads(t *testing.T) {
	heads := "hash head1..head2"
	commits := "commits head1..head2"
	tests := []struct {
		name          string
		release       string
		before, after string
		// heads is what GitLab answers when asked to compare head1 with
		// head2: 404 when empty.
		heads string
		// diffs answers the comparison of each head with the base, when set.
		diffs string
		want  string
		asked []string
	}{
		{name: "18.5, same patch ID, same tree", release: "18.5.1-ee", before: patchA, after: patchA, heads: sameTrees,
			want: "true", asked: []string{"metadata", heads}},
		{name: "18.5, same patch ID, another tree: the change moved", release: "18.5.1-ee", before: patchA, after: patchA, heads: otherTrees,
			want: "false", asked: []string{"metadata", heads, commits}},
		{name: "18.5, same patch ID, the answer is cut short and lists files", release: "18.5.1-ee", before: patchA, after: patchA,
			heads: `{"diffs": [{"old_path": "a", "new_path": "a", "diff": ""}], "compare_timeout": true}`,
			want:  "false", asked: []string{"metadata", heads, commits}},
		{name: "18.5, same patch ID, the answer is cut short and lists none", release: "18.5.1-ee", before: patchA, after: patchA,
			heads: `{"diffs": [], "compare_timeout": true}`,
			want:  "unknown", asked: []string{"metadata", heads, commits}},
		{name: "18.5, same patch ID, an answer without diffs", release: "18.5.1-ee", before: patchA, after: patchA, heads: `{"commits": []}`,
			want: "unknown", asked: []string{"metadata", heads, commits}},
		{name: "18.5, same patch ID, the old head is gone", release: "18.5.1-ee", before: patchA, after: patchA,
			want: "unknown", asked: []string{"metadata", heads, commits}},
		{name: "18.5, another patch ID: the heads are not compared", release: "18.5.1-ee", before: patchA, after: patchB, heads: sameTrees,
			want: "false", asked: []string{"metadata", commits}},

		// Without patch IDs too, and there it is all that is asked: it
		// is exact, where the two diffs may hide a binary file.
		{name: "17.11, same tree", release: "17.11.4", before: patchA, after: patchA, heads: sameTrees,
			want: "true", asked: []string{"metadata", heads}},
		{name: "17.11, another tree", release: "17.11.4", before: patchA, after: patchA, heads: otherTrees, diffs: comparison(sameDiff),
			want: "false", asked: []string{"metadata", heads, commits}},
		{name: "18.5, a cursor without a patch ID, same tree", release: "18.5.1-ee", after: patchA, heads: sameTrees,
			want: "true", asked: []string{heads}},
		{name: "18.5, a cursor without a patch ID, another tree", release: "18.5.1-ee", after: patchA, heads: otherTrees, diffs: comparison(sameDiff),
			want: "false", asked: []string{heads, commits}},
		// When GitLab does not give that comparison, the two diffs decide.
		{name: "17.11, an answer without diffs for the heads, the same diffs", release: "17.11.4", before: patchA, after: patchA,
			heads: `{"commits": []}`, diffs: comparison(sameDiff),
			want: "true", asked: []string{"metadata", heads, "hash base2..head1", "hash base2..head2"}},
		{name: "17.11, the heads cannot be compared, nor the diffs", release: "17.11.4", before: patchA, after: patchA,
			want: "unknown", asked: []string{"metadata", heads, "hash base2..head1", commits}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &differFake{release: tt.release, compare: map[string]string{}}
			if tt.heads != "" {
				f.compare["head1..head2"] = tt.heads
			}
			if tt.diffs != "" {
				f.compare["base2..head1"], f.compare["base2..head2"] = tt.diffs, tt.diffs
			}
			d := &Differ{Client: f.client(t), People: People{Me: "me"}}
			prev, s := amended(tt.before, tt.after)
			// Three polls: everything is asked once, but the commits when
			// GitLab does not have them.
			for range 3 {
				e := onlyPush(t, d, prev, s)
				if got := rebaseOnly(e); got != tt.want || e.BaseMoved {
					t.Errorf("rebase_only = %s, base_moved %v; want %s, false", got, e.BaseMoved, tt.want)
				}
			}
			if got := f.requests(); !slices.Equal(got, tt.asked) {
				t.Errorf("asked %v, want %v", got, tt.asked)
			}
		})
	}
}

// When the base moved, a rebase, equal patch IDs stand and nothing is
// compared: the two heads do not have the same tree anyway.
func TestMovedBaseDoesNotCompareTheHeads(t *testing.T) {
	f := &differFake{release: "18.5.1-ee", compare: map[string]string{"head1..head2": otherTrees}}
	prev, s := pushed(patchA, patchA)
	e := onlyPush(t, &Differ{Client: f.client(t), People: People{Me: "me"}}, prev, s)
	if rebaseOnly(e) != "true" || !e.BaseMoved {
		t.Errorf("push = %+v, want a rebase onto another base", e)
	}
	if got, want := f.requests(), []string{"metadata"}; !slices.Equal(got, want) {
		t.Errorf("asked %v, want %v", got, want)
	}
}

// A comparison that GitLab fails to make must not fail the events: the
// wait would fail at every poll, and never report the push, nor the
// comment or the merge that follows. The push is reported with
// rebase_only absent, and the failure is not asked again at every poll.
func TestFailedComparisonDoesNotFailTheEvents(t *testing.T) {
	commented := func(s *Snapshot) *Snapshot {
		s.Discussions = append(s.Discussions, gitlab.Discussion{ID: "comment-000000000000", Notes: []gitlab.Note{note(40, "alice", 9, "have a look")}})
		return s
	}
	tests := []struct {
		name   string
		status int
		amend  bool
		before string
		// first and then are what the first and the second poll ask.
		first, then []string
	}{
		// A 500 may pass: asked twice, then no more.
		{name: "500, the two diffs", status: 500, before: "",
			first: []string{"hash base1..head1", "hash base1..head1", "commits base2..head2"}, then: []string{"commits base2..head2"}},
		{name: "500, the two heads, then the two diffs", status: 500, amend: true, before: "",
			first: []string{"hash head1..head2", "hash head1..head2", "hash base2..head1", "hash base2..head1", "commits head1..head2"}, then: []string{"commits head1..head2"}},
		{name: "500, the two heads, with patch IDs", status: 500, amend: true, before: patchA,
			first: []string{"metadata", "hash head1..head2", "hash head1..head2", "commits head1..head2"}, then: []string{"commits head1..head2"}},
		// A 403 will not: asked once.
		{name: "403, the two diffs", status: 403, before: "",
			first: []string{"hash base1..head1", "commits base2..head2"}, then: []string{"commits base2..head2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &differFake{release: "18.5.1-ee", compareStatus: tt.status}
			d := &Differ{Client: f.client(t), People: People{Me: "me"}}
			prev, s := pushed(tt.before, patchA)
			if tt.amend {
				prev, s = amended(tt.before, patchA)
			}
			s = commented(s)
			for i, asked := range [][]string{tt.first, tt.then} {
				events, err := d.Events(context.Background(), prev, s)
				if err != nil {
					t.Fatalf("poll %d: err = %v, want the events", i+1, err)
				}
				if got := kinds(events); !slices.Equal(got, []string{"push", "comment"}) || rebaseOnly(events[0]) != "unknown" || events[0].Trigger() != "push" {
					t.Errorf("poll %d: events = %+v, want a push that could not be compared and the comment", i+1, events)
				}
				if got := f.requests(); !slices.Equal(got, asked) {
					t.Errorf("poll %d: asked %v, want %v", i+1, got, asked)
				}
			}
		})
	}
}

// A comparison that GitLab does not answer in time is not asked twice in
// a row: the client waits a minute for each.
func TestComparisonThatTimesOutIsAskedOnce(t *testing.T) {
	f := &differFake{release: "18.5.1-ee", compareHangs: true}
	c := f.client(t)
	c.HTTP.Timeout = 50 * time.Millisecond
	d := &Differ{Client: c, People: People{Me: "me"}}
	prev, s := pushed("", patchA)
	for range 2 {
		if e := onlyPush(t, d, prev, s); rebaseOnly(e) != "unknown" {
			t.Errorf("push = %+v, want one that could not be compared", e)
		}
	}
	want := []string{"hash base1..head1", "commits base2..head2", "commits base2..head2"}
	if got := f.requests(); !slices.Equal(got, want) {
		t.Errorf("asked %v, want %v", got, want)
	}
}

// The command being interrupted is not GitLab failing: the error is
// returned, and nothing is remembered as unknown.
func TestInterruptedComparisonIsAnError(t *testing.T) {
	f := &differFake{release: "18.5.1-ee", compare: map[string]string{
		"base1..head1": comparison(sameDiff),
		"base2..head2": comparison(sameDiff),
	}}
	d := &Differ{Client: f.client(t), People: People{Me: "me"}}
	prev, s := pushed("", patchA)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Events(ctx, prev, s); !errors.Is(err, context.Canceled) {
		t.Errorf("interrupted: err = %v, want context.Canceled", err)
	}
	if e := onlyPush(t, d, prev, s); rebaseOnly(e) != "true" {
		t.Errorf("run again: push = %+v, want a rebase", e)
	}
}
