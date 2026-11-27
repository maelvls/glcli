package mr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
	"github.com/maelvls/glcli/internal/ref"
)

func TestPatchIDIgnoresLineNumbersAndContext(t *testing.T) {
	before := []gitlab.VersionDiff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -10,3 +10,3 @@ func f() {\n ctx\n-old\n+new\n ctx2\n"}}
	rebased := []gitlab.VersionDiff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -42,3 +42,3 @@ func g() {\n other ctx\n-old\n+new  \n changed ctx\n"}}
	changed := []gitlab.VersionDiff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -10,3 +10,3 @@\n ctx\n-old\n+newer\n"}}
	a, _ := PatchID(before)
	b, _ := PatchID(rebased)
	c, _ := PatchID(changed)
	if a != b {
		t.Error("a rebase that keeps the changes must keep the patch ID")
	}
	if a == c {
		t.Error("different changes must give a different patch ID")
	}
	if _, known := PatchID([]gitlab.VersionDiff{{NewPath: "big", TooLarge: true}}); known {
		t.Error("a too-large diff makes the patch ID unknown")
	}
}

func TestCursorRoundTrip(t *testing.T) {
	c := Cursor{SHA: "abc", Version: 3, Base: "def", State: "opened", MaxNote: 42, Unresolved: []string{"x"}, Approvers: []string{"bob"}, Pipeline: "1:running"}
	got, err := DecodeCursor(c.Encode())
	if err != nil || !slices.Equal(got.Unresolved, c.Unresolved) || got.SHA != c.SHA || got.Base != c.Base || got.MaxNote != 42 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := DecodeCursor("garbage"); err == nil {
		t.Fatal("expected an error")
	}
}

func at(min int) time.Time { return time.Date(2026, 11, 26, 10, min, 0, 0, time.UTC) }

func note(id int, user string, min int, body string) gitlab.Note {
	return gitlab.Note{ID: id, Author: gitlab.User{Username: user}, CreatedAt: at(min), UpdatedAt: at(min), Body: body}
}

// fakeCompare answers GET .../repository/compare with the diffs of the
// "base..head" key.
func fakeCompare(t *testing.T, diffs map[string]string) *gitlab.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("from") + ".." + r.URL.Query().Get("to")
		d, ok := diffs[key]
		if !ok {
			http.Error(w, `{"message":"404 Not found"}`, 404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"commits": []map[string]string{{"short_id": "c1", "title": "a commit"}},
			"diffs":   []map[string]string{{"old_path": "a.go", "new_path": "a.go", "diff": d}},
		})
	}))
	t.Cleanup(srv.Close)
	c := gitlab.New(config.Host{Name: "test", Token: "x"})
	c.BaseURL = srv.URL
	return c
}

func snapshot() *Snapshot {
	resolver := gitlab.User{Username: "alice"}
	return &Snapshot{
		Ref: ref.MR{Project: "g/p", IID: 1},
		MR: gitlab.MergeRequest{
			ProjectID: 1, State: "opened", SHA: "head2", WebURL: "https://gitlab.example/g/p/-/merge_requests/1",
			HeadPipeline: &gitlab.PipelineRef{ID: 9, Status: "failed", SHA: "head2"},
		},
		Versions: []gitlab.Version{{ID: 2, HeadCommitSHA: "head2", BaseCommitSHA: "base2", CreatedAt: at(5)}},
		Approvals: gitlab.Approvals{ApprovedBy: []struct {
			User gitlab.User `json:"user"`
		}{{User: gitlab.User{Username: "bob"}}}},
		Discussions: []gitlab.Discussion{
			{ID: "thread-mine-0000000", Notes: []gitlab.Note{
				func() gitlab.Note {
					n := note(10, "me", 1, "please fix")
					n.Resolvable, n.Resolved, n.ResolvedBy = true, true, &resolver
					return n
				}(),
				note(20, "alice", 6, "fixed"),
			}},
			{ID: "system-000000000000", Notes: []gitlab.Note{func() gitlab.Note {
				n := note(21, "alice", 5, "added 1 commit")
				n.System = true
				return n
			}()}},
			{ID: "bot-0000000000000000", Notes: []gitlab.Note{note(22, "project_1_bot_abc", 7, "coverage 80%")}},
			{ID: "mine-new-00000000000", Notes: []gitlab.Note{note(23, "me", 8, "my own comment")}},
		},
	}
}

func kinds(events []Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func TestEventsSinceCursor(t *testing.T) {
	c := fakeCompare(t, map[string]string{
		"base1..head1": "@@ -1 +1 @@\n-old\n+new\n",
		"base2..head2": "@@ -7 +7 @@\n-old\n+newer\n",
		"head1..head2": "@@ -7 +7 @@\n-new\n+newer\n", // the new commits
	})
	prev := Cursor{SHA: "head1", Base: "base1", Version: 1, State: "opened", MaxNote: 10, Unresolved: []string{shortID("thread-mine-0000000")}, Pipeline: "9:running"}
	d := &Differ{Client: c, People: People{Me: "me"}}
	events, err := d.Events(context.Background(), prev, snapshot())
	if err != nil {
		t.Fatal(err)
	}
	// The bot's note and my own note are not comments; the push is by alice.
	if got, want := kinds(events), []string{"push", "comment", "resolved", "approved", "pipeline"}; !slices.Equal(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	push := events[0]
	if push.RebaseOnly == nil || *push.RebaseOnly || !push.BaseMoved || push.By != "alice" || push.Mine || len(push.Commits) != 1 {
		t.Errorf("push = %+v", push)
	}
	cm := events[1].Comment
	if cm.InThread == nil || !cm.InThread.Mine || cm.InThread.StartedBy != "me" || events[1].By != "alice" {
		t.Errorf("comment = %+v", cm)
	}
	if events[2].By != "alice" || events[2].Mine {
		t.Errorf("resolved = %+v", events[2])
	}
	if events[4].Pipeline.Status != "failed" || events[4].Trigger() != "pipeline" {
		t.Errorf("pipeline = %+v", events[4])
	}
}

func TestRebaseOnlyPush(t *testing.T) {
	c := fakeCompare(t, map[string]string{
		"base1..head1": "@@ -1,2 +1,2 @@\n ctx\n-old\n+new\n",
		"base2..head2": "@@ -40,2 +40,2 @@\n other ctx\n-old\n+new\n",
	})
	s := snapshot()
	prev := NewCursor(s)
	prev.SHA, prev.Base, prev.Version = "head1", "base1", 1
	d := &Differ{Client: c, People: People{Me: "me"}}
	events, err := d.Events(context.Background(), prev, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].RebaseOnly == nil || !*events[0].RebaseOnly || events[0].Trigger() != "rebase" || len(events[0].Commits) != 0 {
		t.Fatalf("events = %+v", events)
	}
}

func TestNoEventsAgainstOwnCursor(t *testing.T) {
	s := snapshot()
	d := &Differ{People: People{Me: "me"}}
	events, err := d.Events(context.Background(), NewCursor(s), s)
	if err != nil || len(events) != 0 {
		t.Fatalf("events = %v, err = %v", kinds(events), err)
	}
}

func TestViewAndThreads(t *testing.T) {
	s := snapshot()
	v := NewView(s, People{Me: "me"}, ViewOptions{})
	if v.LastByOthers == nil || v.LastByOthers.Author != "alice" || v.LastByMe == nil || !v.LastByMe.Equal(at(8)) {
		t.Errorf("view = %+v", v)
	}
	if v.Threads.Resolvable != 1 || v.Threads.Unresolved != 0 || v.Approvals.ApprovedByMe {
		t.Errorf("threads/approvals = %+v %+v", v.Threads, v.Approvals)
	}
	threads := Threads(s, People{Me: "me"}, ThreadFilter{Mine: true})
	if len(threads) != 2 {
		t.Errorf("mine: %d threads", len(threads))
	}
	threads = Threads(s, People{Me: "me"}, ThreadFilter{AfterNote: 20})
	if len(threads) != 2 || threads[0].ID != "bot-0000000000000000" {
		t.Errorf("after note 20: %+v", threads)
	}
}

func TestIsBot(t *testing.T) {
	for _, u := range []string{"project_123_bot_abc123", "group_9_bot", "renovate-bot", "ghost"} {
		if !gitlab.IsBot(u) {
			t.Errorf("%s should be a bot", u)
		}
	}
	for _, u := range []string{"mael.valais", "abbott", "robotnik"} {
		if gitlab.IsBot(u) {
			t.Errorf("%s should not be a bot", u)
		}
	}
}
