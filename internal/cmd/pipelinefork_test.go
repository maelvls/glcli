package cmd

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// The merge request of these tests is group/project!1 (project 1), opened
// from a fork, contributor/project (project 2): its pipelines run in the
// fork, and the parent project answers 404 for them.
const (
	forkHeadSHA = "abcdef1234abcdef1234abcdef1234abcdef1234"
	forkOldSHA  = "0123456789012345678901234567890123456789"
)

// forkPipelineRef is a pipeline as the merge request reports it. withURL
// is false for a GitLab that would give no web_url: the project is then
// known by its ID only.
func forkPipelineRef(id, projectID int, sha, status string, withURL bool) string {
	path := map[int]string{1: "group/project", 2: "contributor/project"}[projectID]
	url := ""
	if withURL {
		url = `,"web_url":"https://gitlab.com/` + path + `/-/pipelines/` + strconv.Itoa(id) + `"`
	}
	return `{"id":` + strconv.Itoa(id) + `,"project_id":` + strconv.Itoa(projectID) + `,"sha":"` + sha + `","ref":"feature","status":"` + status + `","source":"merge_request_event"` + url + `}`
}

// forkMR is the merge request from a fork. Its head pipeline (7) runs in
// the fork, as does the pipeline of the previous commit (6); a member of
// the parent project ran pipeline 5 there, for the previous commit too.
// fork is how the API is asked for the fork: by its ID, with or without a
// web_url in what the merge request reports. status is asked each time
// GitLab tells the status of pipeline 7, readable each time something of
// the fork is asked.
func forkMR(withURL bool, status func() string, readable func() bool) (answers func(path string) (string, int), fork string) {
	fork = "projects/2"
	return func(path string) (string, int) {
		if strings.HasPrefix(path, fork+"/") && !readable() {
			return `{"message":"404 Project Not Found"}`, 404
		}
		switch path {
		case waitMR:
			return `{"iid":1,"project_id":1,"sha":"` + forkHeadSHA + `","head_pipeline":` + forkPipelineRef(7, 2, forkHeadSHA, status(), withURL) + `}`, 200
		case waitMR + "/pipelines":
			return `[` + forkPipelineRef(7, 2, forkHeadSHA, status(), withURL) + `,` +
				forkPipelineRef(6, 2, forkOldSHA, "failed", withURL) + `,` +
				forkPipelineRef(5, 1, forkOldSHA, "success", withURL) + `]`, 200
		case fork + "/pipelines/7":
			return forkPipelineRef(7, 2, forkHeadSHA, status(), true), 200
		case fork + "/pipelines/6":
			return forkPipelineRef(6, 2, forkOldSHA, "failed", true), 200
		case fork + "/pipelines/7/jobs":
			return `[{"id":70,"name":"test","stage":"test","status":"failed"}]`, 200
		case fork + "/pipelines/6/jobs":
			return `[{"id":60,"name":"lint","stage":"test","status":"failed"}]`, 200
		case fork + "/pipelines/7/bridges", fork + "/pipelines/6/bridges":
			return `[]`, 200
		case fork + "/jobs/70/trace":
			return "$ make test\nFAIL: TestFork\n", 200
		case fork + "/jobs/60/trace":
			return "$ make lint\nFAIL: lint\n", 200
		case "projects/group/project/pipelines/5":
			return forkPipelineRef(5, 1, forkOldSHA, "success", true), 200
		case "projects/group/project/pipelines/5/jobs", "projects/group/project/pipelines/5/bridges":
			return `[]`, 200
		}
		// Among others: pipelines 6 and 7 asked of the parent project.
		return `{"message":"404 Not found"}`, 404
	}, fork
}

// askedOutside lists the requests for pipelines, jobs and logs made to
// another project than the one whose API path is project.
func (e *waitEnv) askedOutside(project string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for path := range e.seen {
		if strings.HasPrefix(path, project+"/") || strings.Contains(path, "/merge_requests/") {
			continue
		}
		out = append(out, path)
	}
	return out
}

func always(s string) func() string { return func() string { return s } }

func yes() bool { return true }

// forkCommands are the two commands, on the merge request from a fork.
var forkCommands = [][]string{
	{"pipeline", "get", "group/project!1"},
	{"pipeline", "wait", "group/project!1", "--interval", "5s"},
}

// A merge request from a fork: its pipeline is read from the fork, the
// project the merge request reports for it, not from the project of the
// merge request, which answers 404.
func TestPipelineOfAMergeRequestFromAFork(t *testing.T) {
	for _, withURL := range []bool{true, false} {
		for _, args := range forkCommands {
			name := args[1] + map[bool]string{true: ", pipelines with a web_url", false: ", pipelines without web_url"}[withURL]
			t.Run(name, func(t *testing.T) {
				read := 0
				status := func() string {
					// The wait polls a few times before the pipeline ends.
					if read++; args[1] == "wait" && read <= 6 {
						return "running"
					}
					return "failed"
				}
				answers, fork := forkMR(withURL, status, yes)
				e := newWaitEnv(t, answers)
				code, out := e.run(t, args...)
				if code != 0 {
					t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
				}
				line := e.stdout.String()
				id, st, warnings, _ := printed(t, e, out)
				if id != 7 || st != "failed" || string(out["running"]) != "false" || len(warnings) != 0 {
					t.Errorf("want the pipeline 7, failed, without a warning, got %s", line)
				}
				var p struct {
					Project string `json:"project"`
				}
				// The pipeline itself always has a web_url.
				if err := json.Unmarshal(out["pipeline"], &p); err != nil || p.Project != "contributor/project" {
					t.Errorf("want the pipeline in contributor/project, got %s", line)
				}
				var jobs []struct {
					Name string `json:"name"`
					Log  string `json:"log"`
				}
				if err := json.Unmarshal(out["failed_jobs"], &jobs); err != nil || len(jobs) != 1 || jobs[0].Name != "test" || !strings.Contains(jobs[0].Log, "FAIL: TestFork") {
					t.Errorf("want the failed job of the fork with its log, got %s", line)
				}
				for _, path := range []string{"/pipelines/7", "/pipelines/7/jobs", "/pipelines/7/bridges", "/jobs/70/trace"} {
					if e.count(fork+path) == 0 {
						t.Errorf("%s was not asked of the fork", path)
					}
				}
				if outside := e.askedOutside(fork); len(outside) != 0 {
					t.Errorf("requests made outside the fork: %v", outside)
				}
				if args[1] == "wait" && e.count(fork+"/pipelines/7") < 3 {
					t.Errorf("the pipeline was read %d times, want several polls", e.count(fork+"/pipelines/7"))
				}
			})
		}
	}
}

// --sha: each pipeline is read from its own project. The head pipeline is
// in the fork; of the two pipelines of the previous commit, the newest
// (6) is in the fork as well.
func TestPipelineShaOfAMergeRequestFromAFork(t *testing.T) {
	for _, tt := range []struct {
		name, sha string
		id        int
		job       string
	}{
		{"the head commit", forkHeadSHA[:8], 7, "test"},
		{"an older commit", forkOldSHA[:8], 6, "lint"},
	} {
		for _, args := range forkCommands {
			t.Run(args[1]+", "+tt.name, func(t *testing.T) {
				answers, fork := forkMR(true, always("failed"), yes)
				e := newWaitEnv(t, answers)
				code, out := e.run(t, append(args, "--sha", tt.sha)...)
				if code != 0 {
					t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
				}
				id, status, warnings, _ := printed(t, e, out)
				var jobs []struct {
					Name    string `json:"name"`
					Project string `json:"project"`
					Log     string `json:"log"`
				}
				if err := json.Unmarshal(out["failed_jobs"], &jobs); err != nil || id != tt.id || status != "failed" || len(warnings) != 0 ||
					len(jobs) != 1 || jobs[0].Name != tt.job || jobs[0].Project != "contributor/project" || jobs[0].Log == "" {
					t.Errorf("want the pipeline %d and its failed job %s with its log, got %s", tt.id, tt.job, e.stdout.String())
				}
				if outside := e.askedOutside(fork); len(outside) != 0 {
					t.Errorf("requests made outside the fork: %v", outside)
				}
			})
		}
	}
}

// The only pipeline of the commit asked for ran in the parent project (a
// member ran it there), while the head pipeline is in the fork: it is
// read from the parent project.
func TestPipelineOfAForkMergeRequestRunInTheParentProject(t *testing.T) {
	for _, args := range forkCommands {
		t.Run(args[1], func(t *testing.T) {
			answers, _ := forkMR(true, always("failed"), yes)
			e := newWaitEnv(t, with(answers, map[string]func() (string, int){
				waitMR + "/pipelines": func() (string, int) {
					return `[` + forkPipelineRef(7, 2, forkHeadSHA, "failed", true) + `,` + forkPipelineRef(5, 1, forkOldSHA, "success", true) + `]`, 200
				},
			}))
			code, out := e.run(t, append(args, "--sha", forkOldSHA)...)
			if id, status, _, _ := printed(t, e, out); code != 0 || id != 5 || status != "success" {
				t.Fatalf("exit %d, want the pipeline 5, got %s (stderr: %s)", code, e.stdout.String(), e.stderr.String())
			}
			if outside := e.askedOutside("projects/group/project"); len(outside) != 0 {
				t.Errorf("requests made outside the parent project: %v", outside)
			}
		})
	}
}

// forkWarning checks the one warning of a result whose pipeline could not
// be read from the fork.
func forkWarning(t *testing.T, e *waitEnv, warnings []string, status string) {
	t.Helper()
	if len(warnings) != 1 {
		t.Fatalf("want one warning, got %s", e.stdout.String())
	}
	for _, want := range []string{"pipeline 7", "contributor/project", "the fork", "cannot read", "HTTP 404", "(" + status + ")", "failed jobs", "downstream pipelines"} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("the warning lacks %q: %s", want, warnings[0])
		}
	}
}

// The token cannot read the fork: the result is what the merge request
// says of the pipeline, with a warning, not "404 Not found" and exit 1.
func TestPipelineInAForkTheTokenCannotRead(t *testing.T) {
	for _, args := range forkCommands {
		t.Run(args[1], func(t *testing.T) {
			answers, fork := forkMR(true, always("failed"), func() bool { return false })
			e := newWaitEnv(t, answers)
			code, out := e.run(t, args...)
			if code != 0 {
				t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
			}
			line := e.stdout.String()
			id, status, warnings, _ := printed(t, e, out)
			var p struct {
				Project, URL, SHA string
			}
			if err := json.Unmarshal(out["pipeline"], &p); err != nil || id != 7 || status != "failed" ||
				p.Project != "contributor/project" || p.URL != "https://gitlab.com/contributor/project/-/pipelines/7" || p.SHA != forkHeadSHA {
				t.Errorf("want the pipeline 7 as the merge request reports it, got %s", line)
			}
			if string(out["running"]) != "false" || string(out["failed_jobs"]) != "[]" || out["timed_out"] != nil {
				t.Errorf("want a finished pipeline without jobs, got %s", line)
			}
			forkWarning(t, e, warnings, "failed")
			if e.count(fork+"/pipelines/7") == 0 {
				t.Error("the pipeline was not asked of the fork")
			}
		})
	}
}

// The wait on a pipeline in an unreadable fork follows the status the
// merge request reports, read again at each poll, and says so once.
func TestPipelineWaitInAForkTheTokenCannotRead(t *testing.T) {
	read := 0
	answers, _ := forkMR(true, func() string {
		if read++; read <= 4 {
			return "running"
		}
		return "success"
	}, func() bool { return false })
	e := newWaitEnv(t, answers)
	code, out := e.wait(t)
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	id, status, warnings, _ := printed(t, e, out)
	if id != 7 || status != "success" || string(out["running"]) != "false" || out["timed_out"] != nil {
		t.Errorf("want the pipeline 7, success, got %s", e.stdout.String())
	}
	forkWarning(t, e, warnings, "success")
	if n := e.count(waitMR); n < 4 {
		t.Errorf("the merge request was read %d times, want a poll for each status", n)
	}
}

// --timeout while the pipeline of the unreadable fork still runs.
func TestPipelineWaitTimeoutInAForkTheTokenCannotRead(t *testing.T) {
	answers, _ := forkMR(true, always("running"), func() bool { return false })
	e := newWaitEnv(t, answers)
	code, out := e.wait(t, "--timeout", "2m")
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	id, status, warnings, hints := printed(t, e, out)
	if id != 7 || status != "running" || string(out["running"]) != "true" || string(out["timed_out"]) != "true" || len(hints) != 1 {
		t.Errorf("want the pipeline 7, running, timed out, with a hint, got %s", e.stdout.String())
	}
	forkWarning(t, e, warnings, "running")
}

// The fork is readable again after an outage of its own: a 500 from the
// fork is retried, not taken for a fork that cannot be read.
func TestPipelineWaitRetriesAForkThatFails(t *testing.T) {
	asked := 0
	answers, fork := forkMR(true, always("failed"), yes)
	e := newWaitEnv(t, with(answers, map[string]func() (string, int){
		fork + "/pipelines/7": func() (string, int) {
			if asked++; asked <= 2 {
				return outage, 500
			}
			return answers(fork + "/pipelines/7")
		},
	}))
	code, out := e.wait(t)
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	var jobs []json.RawMessage
	id, status, warnings, _ := printed(t, e, out)
	if err := json.Unmarshal(out["failed_jobs"], &jobs); err != nil || id != 7 || status != "failed" || len(warnings) != 0 || len(jobs) != 1 {
		t.Errorf("want the pipeline 7 with its failed job and no warning, got %s", e.stdout.String())
	}
}

// A pipeline of the merge request's own project that answers 404 is an
// error, as before: only a fork is expected to be out of reach.
func TestPipelineNotFoundInTheProjectOfTheMergeRequestIsAnError(t *testing.T) {
	for _, args := range forkCommands {
		t.Run(args[1], func(t *testing.T) {
			e := newWaitEnv(t, func(path string) (string, int) {
				if path == waitMR {
					// project_id is the merge request's, or missing.
					return `{"iid":1,"project_id":1,"sha":"` + forkHeadSHA + `","head_pipeline":` + forkPipelineRef(7, 1, forkHeadSHA, "failed", true) + `}`, 200
				}
				return `{"message":"404 Not found"}`, 404
			})
			code, _ := e.run(t, args...)
			if code != 1 || e.stdout.Len() != 0 || !strings.Contains(e.stderr.String(), "HTTP 404") {
				t.Errorf("exit %d, want 1 and the 404 on stderr; stdout: %s, stderr: %s", code, e.stdout.String(), e.stderr.String())
			}
		})
	}
}
