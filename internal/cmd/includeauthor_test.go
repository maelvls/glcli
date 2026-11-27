package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// oneComment answers for my merge request group/project!1, with a single
// comment by author.
func oneComment(author string) http.HandlerFunc {
	const m = `{"id":10,"iid":1,"project_id":5,"title":"Fix it","state":"opened","author":{"username":"me"},"sha":"abc",
		"web_url":"https://gitlab.com/group/project/-/merge_requests/1","references":{"full":"group/project!1"},
		"created_at":"2026-11-26T09:00:00Z","updated_at":"2026-11-26T09:00:00Z"}`
	return func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case p == "/api/v4/merge_requests" && r.URL.Query().Get("author_username") == "me":
			fmt.Fprint(w, "["+m+"]")
		case p == "/api/v4/merge_requests", p == "/api/v4/todos", strings.HasSuffix(p, "/merge_requests/1/versions"):
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(p, "/merge_requests/1"):
			fmt.Fprint(w, m)
		case strings.HasSuffix(p, "/merge_requests/1/approvals"):
			fmt.Fprint(w, `{}`)
		case strings.HasSuffix(p, "/merge_requests/1/discussions"):
			fmt.Fprintf(w, `[{"id":"d1","individual_note":true,"notes":[{"id":7,"body":"This looks wrong.","author":{"username":%q},"created_at":"2026-11-27T10:00:00Z"}]}]`, author)
		default:
			http.NotFound(w, r)
		}
	}
}

// Whose comment counts as a human comment, in "mr get" and in "inbox":
// bots are left out, --include-author brings one back, and --ignore-author
// wins over it.
func TestIncludeAuthor(t *testing.T) {
	const sa = "service_account_group_12_ab12cd"
	tests := []struct {
		author string
		flags  []string
		counts bool
	}{
		{author: "alice", counts: true},
		{author: "Yann.Le-Bot", counts: true},
		{author: "GitLabDuo", counts: true},
		{author: "GitLabDuo", flags: []string{"--ignore-author", "GitLabDuo"}},
		{author: "GitLab-Security-Bot"},
		{author: sa},
		{author: sa, flags: []string{"--include-author", sa}, counts: true},
		{author: sa, flags: []string{"--include-author", "@" + sa}, counts: true},
		{author: sa, flags: []string{"--include-author", "someone.else"}},
		{author: sa, flags: []string{"--include-author", sa, "--ignore-author", sa}},
	}
	for _, tt := range tests {
		name := strings.Join(append([]string{tt.author}, tt.flags...), " ")

		rt, stdout, stderr := fakeGitLab(t, oneComment(tt.author))
		if code := run(rt, append([]string{"mr", "get", "group/project!1"}, tt.flags...)); code != 0 {
			t.Fatalf("mr get, %s: exit %d (stderr: %s)", name, code, stderr.String())
		}
		var view struct {
			Last *struct {
				Author string `json:"author"`
			} `json:"last_comment_by_others"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &view); err != nil {
			t.Fatalf("mr get, %s: %v: %s", name, err, stdout.String())
		}
		if counted := view.Last != nil && view.Last.Author == tt.author; counted != tt.counts {
			t.Errorf("mr get, %s: comment counted = %v, want %v: %s", name, counted, tt.counts, stdout.String())
		}

		rt, stdout, stderr = fakeGitLab(t, oneComment(tt.author))
		rt.Now = func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) }
		if code := run(rt, append([]string{"inbox"}, tt.flags...)); code != 0 {
			t.Fatalf("inbox, %s: exit %d (stderr: %s)", name, code, stderr.String())
		}
		var box struct {
			Items []struct {
				Reasons []string `json:"reasons"`
			} `json:"items"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &box); err != nil {
			t.Fatalf("inbox, %s: %v: %s", name, err, stdout.String())
		}
		if counted := len(box.Items) == 1 && strings.Join(box.Items[0].Reasons, ",") == "new_comments"; counted != tt.counts || (!tt.counts && len(box.Items) != 0) {
			t.Errorf("inbox, %s: comment counted = %v, want %v: %s", name, counted, tt.counts, stdout.String())
		}
	}
}
