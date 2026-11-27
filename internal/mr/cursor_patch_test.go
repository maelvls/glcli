package mr

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

// The cursor carries GitLab's patch ID of the head version, under the
// same condition as its base: a snapshot that a push did not cut in two.
func TestCursorCarriesThePatchID(t *testing.T) {
	s := snapshot()
	s.Versions[0].PatchIDSHA = patchA
	c := NewCursor(s)
	if c.Patch != patchA || c.Base != "base2" || c.Version != 2 {
		t.Errorf("cursor = %+v, want the patch ID, the base and the version of the head", c)
	}
	got, err := DecodeCursor(c.Encode())
	if err != nil || got.Patch != patchA {
		t.Errorf("decoded = %+v, %v", got, err)
	}

	s.Unsettled = true
	if c := NewCursor(s); c.Patch != "" || c.Base != "" || c.Version != 0 {
		t.Errorf("unsettled: cursor = %+v, want no patch ID, base nor version", c)
	}

	// GitLab has not computed the patch ID yet: nothing is written.
	s = snapshot()
	if c := NewCursor(s); c.Patch != "" || strings.Contains(decoded(t, c.Encode()), `"pi"`) {
		t.Errorf("no patch ID: cursor = %s", decoded(t, c.Encode()))
	}
}

func decoded(t *testing.T, cursor string) string {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A cursor printed before the patch ID was added still works: the diffs
// are compared, as before.
func TestCursorWithoutPatchIDStillWorks(t *testing.T) {
	old := base64.RawURLEncoding.EncodeToString([]byte(
		`{"m":"1!0","s":"head1","v":1,"b":"base1","st":"opened","n":23,"a":["bob"],"p":"9:failed"}`))
	prev, err := DecodeCursor(old)
	if err != nil || prev.Patch != "" || prev.Base != "base1" || prev.SHA != "head1" {
		t.Fatalf("cursor = %+v, %v", prev, err)
	}
	for _, tt := range []struct{ newDiff, want string }{{sameDiff, "true"}, {otherDiff, "false"}} {
		f := &differFake{release: "18.5.1-ee", compare: map[string]string{
			"base1..head1": comparison(sameDiff),
			"base2..head2": comparison(tt.newDiff),
		}}
		s := snapshot()
		s.Versions[0].PatchIDSHA = patchA
		if err := prev.Check(s); err != nil {
			t.Fatal(err)
		}
		e := onlyPush(t, &Differ{Client: f.client(t), People: People{Me: "me"}}, prev, s)
		if got := rebaseOnly(e); got != tt.want {
			t.Errorf("rebase_only = %s, want %s", got, tt.want)
		}
		if got := f.requests(); !slices.Contains(got, "hash base1..head1") || slices.Contains(got, "metadata") {
			t.Errorf("asked %v, want the diffs compared and the version not asked", got)
		}
		// The cursor printed next carries the patch ID.
		if next := NewCursor(s); next.Patch != patchA {
			t.Errorf("next cursor = %+v", next)
		}
	}
}

// A cursor is passed on a command line: the patch ID adds 64 characters
// to it (48 bytes of JSON, in base64).
func TestCursorSize(t *testing.T) {
	sha := func(c string) string { return strings.Repeat(c, 40) }
	c := Cursor{
		MR: "12345678!4321", SHA: sha("a"), Version: 1234567, Base: sha("b"), State: "opened", MaxNote: 2345678901,
		Unresolved: []string{"0123456789ab", "123456789abc", "23456789abcd"},
		Approvers:  []string{"alice.martin", "bob"}, Pipeline: "1234567890:success",
	}
	before := len(c.Encode())
	c.Patch = sha("c")
	after := len(c.Encode())
	t.Logf("cursor: %d characters without the patch ID, %d with it", before, after)
	if after-before != 64 {
		t.Errorf("the patch ID adds %d characters, want 64", after-before)
	}
	// This one has three unresolved threads, two approvers and large IDs.
	if after > 512 {
		t.Errorf("cursor of %d characters: too long for a command line argument", after)
	}
}
