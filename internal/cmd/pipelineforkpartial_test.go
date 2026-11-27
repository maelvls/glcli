package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

// GitLab shows the pipeline of the fork and refuses its jobs, or its
// trigger jobs: the result is the pipeline as read from the fork, with
// what could be read of it, not the little the merge request says. The
// warning names what is missing, and does not claim that the fork cannot
// be read.
func TestPipelineInAForkReadableInPart(t *testing.T) {
	for _, tt := range []struct {
		name    string
		refused string
		jobs    int
		warning []string
		not     []string
	}{
		{"its jobs are refused", "/pipelines/7/jobs", 0,
			[]string{"its failed jobs", `It failed, and "failed_jobs" does not say why: see https://gitlab.com/contributor/project/-/pipelines/7`}, []string{"its own downstream pipelines"}},
		{"its trigger jobs are refused", "/pipelines/7/bridges", 1,
			[]string{"its own downstream pipelines"}, []string{"its failed jobs", "It failed"}},
	} {
		for _, args := range forkCommands {
			t.Run(args[1]+", "+tt.name, func(t *testing.T) {
				// The merge request says "running": only the fork knows
				// that the pipeline has failed.
				answers, fork := forkMR(true, always("failed"), yes)
				e := newWaitEnv(t, with(answers, map[string]func() (string, int){
					waitMR: func() (string, int) {
						return `{"iid":1,"project_id":1,"sha":"` + forkHeadSHA + `","head_pipeline":` + forkPipelineRef(7, 2, forkHeadSHA, "running", true) + `}`, 200
					},
					fork + tt.refused: func() (string, int) { return `{"message":"403 Forbidden"}`, 403 },
				}))
				code, out := e.run(t, args...)
				if code != 0 {
					t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
				}
				line := e.stdout.String()
				id, status, warnings, _ := printed(t, e, out)
				var p struct{ Unreadable bool }
				if err := json.Unmarshal(out["pipeline"], &p); err != nil || id != 7 || status != "failed" || !p.Unreadable || string(out["running"]) != "false" {
					t.Errorf("want the pipeline 7 as the fork gives it, failed, with \"unreadable\": true, got %s", line)
				}
				var jobs []struct {
					Name, Log string
				}
				if err := json.Unmarshal(out["failed_jobs"], &jobs); err != nil || len(jobs) != tt.jobs || (len(jobs) == 1 && (jobs[0].Name != "test" || jobs[0].Log == "")) {
					t.Errorf("want %d failed job with its log, got %s", tt.jobs, line)
				}
				if len(warnings) != 1 {
					t.Fatalf("want one warning, got %s", line)
				}
				for _, want := range append(tt.warning, "pipeline 7 of this merge request", "contributor/project", "cannot read all of it", "HTTP 403") {
					if !strings.Contains(warnings[0], want) {
						t.Errorf("the warning lacks %q: %s", want, warnings[0])
					}
				}
				for _, not := range append(tt.not, "the merge request reports", "which this token cannot read") {
					if strings.Contains(warnings[0], not) {
						t.Errorf("the warning must not have %q: %s", not, warnings[0])
					}
				}
				// Both were asked for: one refusal does not spare the other.
				for _, path := range []string{"/pipelines/7/jobs", "/pipelines/7/bridges"} {
					if e.count(fork+path) == 0 {
						t.Errorf("%s was not asked of the fork", path)
					}
				}
			})
		}
	}
}
