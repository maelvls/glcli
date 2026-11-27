package mr

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// stateEvent moves snapshot() from opened to what change makes of it, and
// returns the one state event.
func stateEvent(t *testing.T, change func(m *gitlab.MergeRequest)) Event {
	t.Helper()
	s := snapshot()
	prev := NewCursor(s)
	change(&s.MR)
	d := &Differ{People: People{Me: "me"}}
	events, err := d.Events(context.Background(), prev, s)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(events); len(got) != 1 || got[0] != "state" || events[0].PreviousState != "opened" || events[0].Trigger() != "state" {
		t.Fatalf("events = %+v, want one state event", events)
	}
	return events[0]
}

func TestMergedStateEventNamesTheMerger(t *testing.T) {
	alice, me := &gitlab.User{Username: "alice"}, &gitlab.User{Username: "me"}
	mergedAt := at(30)
	for _, tc := range []struct {
		name                string
		mergeUser, mergedBy *gitlab.User
		by                  string
		mine                bool
	}{
		{name: "merge_user only", mergeUser: alice, by: "alice"},
		{name: "merged_by only (older instance)", mergedBy: alice, by: "alice"},
		{name: "merge_user wins over merged_by", mergeUser: me, mergedBy: alice, by: "me", mine: true},
		{name: "merged_by only, by me", mergedBy: me, by: "me", mine: true},
		{name: "neither"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			change := func(m *gitlab.MergeRequest) {
				m.State, m.MergeUser, m.MergedBy = "merged", tc.mergeUser, tc.mergedBy
				m.MergedAt, m.MergeCommitSHA = &mergedAt, "mergesha"
			}
			e := stateEvent(t, change)
			if e.State != "merged" || e.By != tc.by || e.Mine != tc.mine {
				t.Errorf("event = %+v, want by %q, mine %v", e, tc.by, tc.mine)
			}
			// When and which commit do not depend on knowing who.
			if e.At == nil || !e.At.Equal(mergedAt) || e.SHA != "mergesha" {
				t.Errorf("event = %+v, want at %v and sha mergesha", e, mergedAt)
			}

			s := snapshot()
			change(&s.MR)
			if v := NewView(s, People{Me: "me"}, ViewOptions{}); v.MergedBy != tc.by || v.ClosedBy != "" || v.ClosedAt != nil {
				t.Errorf("view: merged_by %q, closed_by %q, closed_at %v", v.MergedBy, v.ClosedBy, v.ClosedAt)
			}
		})
	}
}

func TestClosedStateEventNamesWhoClosed(t *testing.T) {
	closedAt := at(40)
	for _, tc := range []struct {
		name     string
		closedBy *gitlab.User
		by       string
		mine     bool
	}{
		{name: "closed by me", closedBy: &gitlab.User{Username: "me"}, by: "me", mine: true},
		{name: "closed by someone else", closedBy: &gitlab.User{Username: "alice"}, by: "alice"},
		{name: "no closed_by"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			change := func(m *gitlab.MergeRequest) {
				m.State, m.ClosedBy, m.ClosedAt = "closed", tc.closedBy, &closedAt
			}
			e := stateEvent(t, change)
			if e.State != "closed" || e.By != tc.by || e.Mine != tc.mine || e.SHA != "" {
				t.Errorf("event = %+v, want by %q, mine %v", e, tc.by, tc.mine)
			}
			if e.At == nil || !e.At.Equal(closedAt) {
				t.Errorf("at = %v, want %v", e.At, closedAt)
			}

			s := snapshot()
			change(&s.MR)
			v := NewView(s, People{Me: "me"}, ViewOptions{})
			if v.ClosedBy != tc.by || v.ClosedAt == nil || !v.ClosedAt.Equal(closedAt) || v.MergedBy != "" {
				t.Errorf("view: closed_by %q, closed_at %v, merged_by %q", v.ClosedBy, v.ClosedAt, v.MergedBy)
			}
		})
	}
}

// A reopened merge request may still carry who closed it, and who set
// auto-merge: neither is who reopened it.
func TestReopenedStateEventNamesNobody(t *testing.T) {
	closedAt := at(40)
	s := snapshot()
	s.MR.ClosedBy, s.MR.ClosedAt = &gitlab.User{Username: "me"}, &closedAt
	s.MR.MergeUser = &gitlab.User{Username: "me"}
	prev := NewCursor(s)
	prev.State = "closed"
	d := &Differ{People: People{Me: "me"}}
	events, err := d.Events(context.Background(), prev, s)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(events); len(got) != 1 || got[0] != "state" {
		t.Fatalf("events = %+v, want one state event", events)
	}
	if e := events[0]; e.State != "opened" || e.PreviousState != "closed" || e.By != "" || e.Mine || e.At != nil {
		t.Errorf("event = %+v, want a reopen by nobody", e)
	}
	if v := NewView(s, People{Me: "me"}, ViewOptions{}); v.ClosedBy != "" || v.ClosedAt != nil || v.MergedBy != "" {
		t.Errorf("view of an open merge request: closed_by %q, closed_at %v, merged_by %q", v.ClosedBy, v.ClosedAt, v.MergedBy)
	}
}

// The struct tags are the field names of GitLab's merge request API.
func TestMergeRequestDecodesMergeUserAndClosedBy(t *testing.T) {
	const merged = `{
		"iid": 1, "state": "merged",
		"merged_by": {"id": 87854, "name": "Douwe Maan", "username": "DouweM"},
		"merge_user": {"id": 12, "name": "Alice", "username": "alice"},
		"merged_at": "2018-09-07T11:16:17.520Z",
		"closed_by": null, "closed_at": null
	}`
	var m gitlab.MergeRequest
	if err := json.Unmarshal([]byte(merged), &m); err != nil {
		t.Fatal(err)
	}
	if m.MergeUser == nil || m.MergeUser.Username != "alice" || m.MergedBy == nil || m.MergedBy.Username != "DouweM" || m.ClosedBy != nil {
		t.Errorf("merge_user %+v, merged_by %+v, closed_by %+v", m.MergeUser, m.MergedBy, m.ClosedBy)
	}
	if u := m.Merger(); u == nil || u.Username != "alice" {
		t.Errorf("Merger() = %+v, want merge_user", u)
	}

	const closed = `{
		"iid": 2, "state": "closed",
		"merged_by": null, "merge_user": null, "merged_at": null,
		"closed_by": {"id": 3, "name": "Bob", "username": "bob"},
		"closed_at": "2018-09-08T10:00:00.000Z"
	}`
	m = gitlab.MergeRequest{}
	if err := json.Unmarshal([]byte(closed), &m); err != nil {
		t.Fatal(err)
	}
	if m.ClosedBy == nil || m.ClosedBy.Username != "bob" || m.ClosedAt == nil || m.Merger() != nil {
		t.Errorf("closed_by %+v, closed_at %v, Merger() %+v", m.ClosedBy, m.ClosedAt, m.Merger())
	}

	// An instance older than merge_user does not send the key at all.
	m = gitlab.MergeRequest{}
	if err := json.Unmarshal([]byte(`{"state": "merged", "merged_by": {"username": "carol"}}`), &m); err != nil {
		t.Fatal(err)
	}
	if u := m.Merger(); u == nil || u.Username != "carol" {
		t.Errorf("Merger() = %+v, want the merged_by fallback", u)
	}
}
