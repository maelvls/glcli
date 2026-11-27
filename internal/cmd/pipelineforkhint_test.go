package cmd

import (
	"strings"
	"testing"
)

// --sha names an older commit of a merge request whose fork the token
// cannot read: GitLab gives the head pipeline, in the fork, and lists none
// of the fork's pipelines. The hint does not only say that no pipeline ran
// on the commit.
func TestPipelineShaWhenTheForkCannotBeRead(t *testing.T) {
	for _, tt := range []struct {
		name    string
		project int // of the head pipeline
		fork    bool
	}{
		{"head pipeline in the fork", 2, true},
		{"head pipeline in the project of the merge request", 1, false},
	} {
		for _, args := range [][]string{
			{"pipeline", "get", "group/project!1", "--sha", forkOldSHA[:8]},
			{"pipeline", "wait", "group/project!1", "--interval", "5s", "--sha", forkOldSHA[:8], "--create-timeout", "1m"},
		} {
			t.Run(args[1]+", "+tt.name, func(t *testing.T) {
				e := newWaitEnv(t, func(path string) (string, int) {
					switch path {
					case waitMR:
						return `{"iid":1,"project_id":1,"sha":"` + forkHeadSHA + `","head_pipeline":` + forkPipelineRef(7, tt.project, forkHeadSHA, "success", true) + `}`, 200
					case waitMR + "/pipelines":
						return `[]`, 200
					}
					return `{"message":"404 Project Not Found"}`, 404
				})
				code, out := e.run(t, args...)
				if code != 0 {
					t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
				}
				_, _, _, hints := printed(t, e, out)
				if len(hints) != 1 || !strings.Contains(hints[0], "None of the 20 most recent pipelines of this merge request ran on "+forkOldSHA[:8]) {
					t.Fatalf("want the hint about the commit, got %s", e.stdout.String())
				}
				if got := strings.Contains(hints[0], "GitLab does not list the pipelines of a fork that this token cannot read"); got != tt.fork {
					t.Errorf("the hint speaks of the fork: %v, want %v: %s", got, tt.fork, hints[0])
				}
			})
		}
	}
}
