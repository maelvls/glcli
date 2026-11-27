package mr

import (
	"context"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// stateEvents are the events between a cursor taken in state from (as
// GitLab names it) and the same merge request in state to.
func stateEvents(t *testing.T, from, to string) ([]Event, Cursor) {
	t.Helper()
	before := snapshot()
	before.MR.State = from
	s := snapshot()
	s.MR.State = to
	if to == "merged" {
		mergedAt := at(30)
		s.MR.MergedAt, s.MR.MergeUser, s.MR.MergeCommitSHA = &mergedAt, &gitlab.User{Username: "alice"}, "mergesha"
	}
	events, err := (&Differ{People: People{Me: "me"}}).Events(context.Background(), NewCursor(before), s)
	if err != nil {
		t.Fatal(err)
	}
	return events, NewCursor(s)
}

// "locked" is short-lived and transitional: GitLab is merging. It is not
// reported, and the cursor keeps "opened", so that the merge is reported
// once, as opened to merged, whichever polls catch the lock.
func TestLockedIsNotAState(t *testing.T) {
	events, c := stateEvents(t, "opened", "locked")
	if len(events) != 0 || c.State != "opened" {
		t.Errorf("opened, then locked: events = %+v, cursor state %q; want none and opened", events, c.State)
	}

	// The cursor of a locked merge request, then the merge.
	events, c = stateEvents(t, "locked", "merged")
	if len(events) != 1 || events[0].Kind != "state" || events[0].State != "merged" || events[0].PreviousState != "opened" ||
		events[0].By != "alice" || events[0].SHA != "mergesha" || events[0].At == nil {
		t.Errorf("locked, then merged: events = %+v, want one merge, from opened", events)
	}
	if c.State != "merged" {
		t.Errorf("cursor state %q, want merged", c.State)
	}

	// The merge failed: opened again, and nothing happened.
	if events, _ := stateEvents(t, "locked", "opened"); len(events) != 0 {
		t.Errorf("locked, then opened: events = %+v, want none", events)
	}
	if events, _ := stateEvents(t, "locked", "locked"); len(events) != 0 {
		t.Errorf("locked, still locked: events = %+v, want none", events)
	}

	// Reopened and merging at once: a reopen, not a "locked".
	events, c = stateEvents(t, "closed", "locked")
	if len(events) != 1 || events[0].State != "opened" || events[0].PreviousState != "closed" || c.State != "opened" {
		t.Errorf("closed, then locked: events = %+v, cursor state %q; want one reopen", events, c.State)
	}

	// The other states are reported as before.
	for _, tt := range [][2]string{{"opened", "merged"}, {"opened", "closed"}, {"closed", "opened"}} {
		events, c := stateEvents(t, tt[0], tt[1])
		if len(events) != 1 || events[0].State != tt[1] || events[0].PreviousState != tt[0] || c.State != tt[1] {
			t.Errorf("%s, then %s: events = %+v, cursor state %q", tt[0], tt[1], events, c.State)
		}
	}
}

// A cursor printed by an older glcli while the merge request was locked
// says "locked": the merge that follows is reported once all the same.
func TestCursorThatSaysLocked(t *testing.T) {
	s := snapshot()
	prev := NewCursor(s)
	prev.State = "locked"
	d := &Differ{People: People{Me: "me"}}
	if events, err := d.Events(context.Background(), prev, s); err != nil || len(events) != 0 {
		t.Errorf("locked in the cursor, opened now: events = %+v, %v; want none", events, err)
	}
	s.MR.State = "merged"
	events, err := d.Events(context.Background(), prev, s)
	if err != nil || len(events) != 1 || events[0].State != "merged" || events[0].PreviousState != "opened" {
		t.Errorf("locked in the cursor, merged now: events = %+v, %v", events, err)
	}
}

// The state GitLab gives is still shown as it is.
func TestViewShowsLocked(t *testing.T) {
	s := snapshot()
	s.MR.State = "locked"
	v := NewView(s, People{Me: "me"}, ViewOptions{})
	c, err := DecodeCursor(v.Cursor)
	if err != nil || v.State != "locked" || c.State != "opened" {
		t.Errorf("view state %q, cursor state %q, %v; want locked and opened", v.State, c.State, err)
	}
}
