package mr

import (
	"context"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

func id(t *testing.T, diffs ...gitlab.VersionDiff) (string, bool) {
	t.Helper()
	return PatchID(diffs)
}

func TestPatchIDSeesRealChanges(t *testing.T) {
	f := func(diff string) gitlab.VersionDiff { return gitlab.VersionDiff{OldPath: "x", NewPath: "x", Diff: diff} }
	// An indentation fix is a change (YAML, Python).
	a, _ := id(t, f("@@ -1 +1 @@\n-a:\n+  b: 1\n"))
	b, _ := id(t, f("@@ -1 +1 @@\n-a:\n+    b: 1\n"))
	if a == b {
		t.Error("indentation changes must change the patch ID")
	}
	// Trailing whitespace alone is not.
	c, _ := id(t, f("@@ -1 +1 @@\n-a:\n+  b: 1   \n"))
	if a != c {
		t.Error("trailing whitespace must not change the patch ID")
	}
	// Lines starting with -- or ++ are changes (GitLab diffs have no ---/+++ headers).
	d, _ := id(t, f("@@ -1 +0,0 @@\n-- a\n"))
	e, _ := id(t, f("@@ -1 +0,0 @@\n-- b\n"))
	if d == e {
		t.Error("removing different SQL comments must give different patch IDs")
	}
	// A binary or mode-only change has no lines: unknown.
	if _, known := id(t, gitlab.VersionDiff{OldPath: "logo.png", NewPath: "logo.png", Diff: ""}); known {
		t.Error("a change without lines must be unknown")
	}
	// A pure rename has no lines but is known.
	if _, known := id(t, gitlab.VersionDiff{OldPath: "a", NewPath: "b", RenamedFile: true}); !known {
		t.Error("a pure rename must be known")
	}
}

func TestCursorCheck(t *testing.T) {
	s := snapshot()
	s.MR.IID = 1
	c := NewCursor(s)
	if err := c.Check(s); err != nil {
		t.Fatal(err)
	}
	other := snapshot()
	other.MR.IID = 2
	if err := c.Check(other); err == nil {
		t.Fatal("a cursor of another merge request must be refused")
	}
}

func TestUnsettledSnapshotHasNoBase(t *testing.T) {
	s := snapshot()
	s.Unsettled = true
	if c := NewCursor(s); c.Base != "" || c.Version != 0 {
		t.Fatalf("an unsettled snapshot must not pair its head with a version: %+v", c)
	}
}

func TestReopenedThreadIsAttributed(t *testing.T) {
	s := snapshot()
	// The resolved thread of snapshot() is reopened by my reply.
	s.Discussions[0].Notes[0].Resolved = false
	s.Discussions[0].Notes = append(s.Discussions[0].Notes, note(30, "me", 9, "not fixed after all"))
	prev := NewCursor(snapshot())
	d := &Differ{People: People{Me: "me"}}
	events, err := d.Events(context.Background(), prev, s)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == "unresolved" {
			if e.By != "me" || !e.Mine {
				t.Fatalf("unresolved = %+v", e)
			}
			return
		}
	}
	t.Fatalf("no unresolved event in %v", kinds(events))
}
