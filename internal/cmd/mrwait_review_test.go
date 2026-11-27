package cmd

import (
	"slices"
	"strings"
	"testing"
)

// A change moved to another place of its file, where the lines around
// it are the same, keeps GitLab's patch ID: timeout 30 to 60 under
// staging:, amended to be under production:. On the same base, the two
// heads are compared, and the push ends the wait.
func TestMRWaitMovedChangeEndsTheWait(t *testing.T) {
	const moved = `{"commits": [{"short_id": "c2", "title": "bump production instead"}], "diffs": [{"old_path": "values.yaml", "new_path": "values.yaml", "a_mode": "100644", "b_mode": "100644", "diff": "@@ -3,7 +3,7 @@ staging:\n   replicas: 2\n   cpu: 500m\n   memory: 1Gi\n-  timeout: 60\n+  timeout: 30\n   retries: 3\n   backoff: 2\n   log: info\n@@ -12,7 +12,7 @@ production:\n   replicas: 2\n   cpu: 500m\n   memory: 1Gi\n-  timeout: 30\n+  timeout: 60\n   retries: 3\n   backoff: 2\n   log: info\n"}]}`
	f := newMRWaitFake(t, "18.5.1-ee")
	opened := mrWaitOpened()
	f.play(opened)
	start := f.get()

	// Same base, same patch ID.
	amended := opened.push(2, "head2", "base1", mrWaitPatchA, 5)
	f.compare["head1..head2"] = moved
	f.play(opened, amended)
	out := f.wait(start.Cursor, "--timeout", "1m")
	if !slices.Equal(out.TriggeredBy, []string{"push"}) || out.TimedOut || len(out.Events) != 1 || out.Events[0].RebaseOnly == nil || *out.Events[0].RebaseOnly {
		t.Errorf("a moved change: %s; want a push with rebase_only false", out.raw)
	}
	if got, want := f.requests(), []string{"metadata", "hash head1..head2", "commits head1..head2"}; !slices.Equal(got, want) {
		t.Errorf("asked %v, want %v", got, want)
	}

	// A reword: the same tree, and the wait goes on.
	f.compare["head1..head2"] = `{"commits": [], "diffs": [], "compare_timeout": false}`
	f.play(opened, amended)
	if out := f.wait(start.Cursor, "--timeout", "1m"); !out.TimedOut || len(out.TriggeredBy) != 0 {
		t.Errorf("a reword: %s; want a timeout", out.raw)
	}
	if got, want := f.requests(), []string{"hash head1..head2"}; !slices.Equal(got, want) {
		t.Errorf("asked %v, want %v: once for the whole wait", got, want)
	}
}

// When GitLab fails to compare, the wait must not fail at every poll for
// ever: the push is reported with rebase_only absent, and so is what
// else happened.
func TestMRWaitReportsThePushWhenGitLabCannotCompare(t *testing.T) {
	for _, status := range []int{500, 403} {
		f := newMRWaitFake(t, "17.11.4")
		f.compareStatus = status
		opened := mrWaitOpened()
		f.play(opened)
		start := f.get()

		pushed := opened.push(2, "head2", "base2", mrWaitPatchA, 5).note(12, "bob", 6, "rebased, have a look", false)
		f.play(opened, pushed)
		out := f.wait(start.Cursor)
		if !slices.Equal(out.TriggeredBy, []string{"push", "comment"}) || !slices.Equal(mrWaitKinds(out.Events), []string{"push", "comment"}) {
			t.Fatalf("compare answers %d: %s; want the push and the comment", status, out.raw)
		}
		if out.Events[0].RebaseOnly != nil || strings.Contains(out.raw, "rebase_only") || f.fetches() != 2 {
			t.Errorf("compare answers %d: %s after %d fetches; want rebase_only absent after 2", status, out.raw, f.fetches())
		}
		// mr get does not fail either.
		f.play(pushed)
		if got := f.get("--cursor", start.Cursor); !slices.Equal(mrWaitKinds(got.Events), []string{"push", "comment"}) {
			t.Errorf("compare answers %d: mr get: %s; want the push and the comment", status, got.raw)
		}
	}
}

// GitLab computes the patch ID of a new diff version just after it
// creates it. A poll in between does not report the push yet: the patch
// ID decides at the next one, nothing is compared, and a rebase does not
// end the wait.
func TestMRWaitWaitsForThePatchID(t *testing.T) {
	f := newMRWaitFake(t, "18.5.1-ee")
	// What the two diffs would say: the target branch added lines above
	// the change, which the strict comparison takes for a change.
	f.compare["base1..head1"] = mrWaitComparison("@@ -1 +1 @@\n-old\n+new\n")
	f.compare["base2..head2"] = mrWaitComparison("@@ -5 +5 @@\n-old\n+new\n")
	opened := mrWaitOpened()
	f.play(opened)
	start := f.get()

	early := opened.push(2, "head2", "base2", "", 5)
	rebased := opened.push(2, "head2", "base2", mrWaitPatchA, 5)
	f.play(opened, early, rebased)
	out := f.wait(start.Cursor, "--on", "rebase")
	if !slices.Equal(out.TriggeredBy, []string{"rebase"}) || len(out.Events) != 1 || out.Events[0].RebaseOnly == nil || !*out.Events[0].RebaseOnly {
		t.Errorf("patch ID one poll late: %s; want a rebase", out.raw)
	}
	if got := f.fetches(); got != 3 {
		t.Errorf("fetched %d times, want 3: nothing, the push without its patch ID, the push with it", got)
	}
	if got, want := f.requests(), []string{"metadata"}; !slices.Equal(got, want) {
		t.Errorf("asked %v, want %v", got, want)
	}
	// The cursor printed carries the patch ID: the next push is told
	// from it too.
	f.play(rebased)
	if again := f.get("--cursor", out.MR.Cursor); len(again.Events) != 0 || out.MR.Cursor != again.Cursor {
		t.Errorf("mr get from the new cursor: %s; want no event and the same cursor", again.raw)
	}

	// GitLab never computes it: after two polls the diffs decide.
	f.play(opened, early)
	out = f.wait(start.Cursor)
	if !slices.Equal(out.TriggeredBy, []string{"push"}) || len(out.Events) != 1 || out.Events[0].RebaseOnly == nil || *out.Events[0].RebaseOnly {
		t.Errorf("no patch ID: %s; want a push told from the diffs", out.raw)
	}
	if got := f.fetches(); got != 4 {
		t.Errorf("fetched %d times, want 4: nothing, two polls for the patch ID, then the diffs", got)
	}
	if got, want := f.requests(), []string{"hash base1..head1", "hash base2..head2", "commits base2..head2"}; !slices.Equal(got, want) {
		t.Errorf("asked %v, want %v", got, want)
	}
}
