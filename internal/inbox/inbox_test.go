package inbox

import (
	"slices"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/gitlab"
	"github.com/maelvls/glcli/internal/mr"
	"github.com/maelvls/glcli/internal/ref"
)

func at(min int) time.Time { return time.Date(2026, 11, 26, 10, min, 0, 0, time.UTC) }

func note(id int, user string, min int, body string, system bool) gitlab.Note {
	return gitlab.Note{ID: id, Author: gitlab.User{Username: user}, CreatedAt: at(min), Body: body, System: system}
}

func snap(author string, notes ...gitlab.Note) *mr.Snapshot {
	var ds []gitlab.Discussion
	for _, n := range notes {
		ds = append(ds, gitlab.Discussion{ID: "d", Notes: []gitlab.Note{n}})
	}
	return &mr.Snapshot{
		Ref:         ref.MR{Project: "g/p", IID: 1},
		MR:          gitlab.MergeRequest{State: "opened", Author: gitlab.User{Username: author}, CreatedAt: at(0), SHA: "h"},
		Versions:    []gitlab.Version{{ID: 1, CreatedAt: at(2)}},
		Discussions: ds,
	}
}

var me = mr.People{Me: "me"}

func TestReviewReasons(t *testing.T) {
	tests := []struct {
		name string
		s    *mr.Snapshot
		want []string
	}{
		{"never reviewed", snap("bob", note(1, "bob", 1, "requested review from @me", true)), []string{"review_requested"}},
		{"reviewed, nothing since", snap("bob", note(1, "bob", 1, "requested review from @me", true), note(2, "me", 3, "nit", false)), nil},
		{"pushed after my review", func() *mr.Snapshot {
			s := snap("bob", note(2, "me", 1, "nit", false))
			s.Versions[0].CreatedAt = at(5)
			return s
		}(), []string{"new_commits"}},
		{"re-requested", snap("bob", note(2, "me", 3, "nit", false), note(3, "bob", 4, "requested review from @me and @alice", true)), []string{"review_requested"}},
		{"mentioned after my comment", snap("bob", note(2, "me", 3, "nit", false), note(3, "alice", 4, "what do you think @me?", false)), []string{"new_replies"}},
		{"mention of a longer username is not me", snap("bob", note(2, "me", 3, "nit", false), note(3, "alice", 4, "@meg look", false)), nil},
		{"bot comments do not count", snap("bob", note(2, "me", 3, "nit", false), note(3, "project_1_bot_x", 4, "@me coverage", false)), nil},
	}
	for _, tt := range tests {
		got := reviewItem(tt.s, me)
		if !slices.Equal(got.Reasons, tt.want) {
			t.Errorf("%s: reasons = %v, want %v", tt.name, got.Reasons, tt.want)
		}
	}
}

func TestAuthoredReasons(t *testing.T) {
	s := snap("me", note(1, "me", 1, "added 1 commit", true), note(2, "alice", 3, "looks wrong", false))
	s.MR.HeadPipeline = &gitlab.PipelineRef{Status: "failed", SHA: "h", UpdatedAt: at(4)}
	s.PipelineIsHead = true
	got := authoredItem(s, me)
	if !slices.Equal(got.Reasons, []string{"new_comments", "pipeline_failed"}) || !got.At.Equal(at(4)) {
		t.Errorf("reasons = %v at %v", got.Reasons, got.At)
	}
	s = snap("me", note(2, "alice", 3, "looks wrong", false), note(4, "me", 5, "fixed", false))
	s.MR.DetailedMergeStatus = "mergeable"
	if got := authoredItem(s, me); !slices.Equal(got.Reasons, []string{"ready_to_merge"}) {
		t.Errorf("reasons = %v", got.Reasons)
	}
	// ready_to_merge dates from the last approval, not from updated_at.
	s = snap("me", note(4, "alice", 6, "approved this merge request", true))
	s.MR.DetailedMergeStatus, s.MR.UpdatedAt = "mergeable", at(50)
	if got := authoredItem(s, me); !got.At.Equal(at(6)) {
		t.Errorf("ready_to_merge at %v, want %v", got.At, at(6))
	}
}

func TestMentions(t *testing.T) {
	tests := map[string]bool{
		"@me":                true,
		"thanks @me.":        true,
		"cc @mex and @me":    true,
		"@me.valais":         false,
		"@meg":               false,
		"email me@me.com":    false,
		"(@me) what do you?": true,
	}
	for body, want := range tests {
		if got := mentions(body, "me"); got != want {
			t.Errorf("mentions(%q) = %v, want %v", body, got, want)
		}
	}
}
