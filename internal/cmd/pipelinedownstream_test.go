package cmd

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// The pipeline of these tests (7, of group/project!1) has succeeded. Its
// trigger job "e2e" started pipeline 20 in group/e2e (project 6), whose
// trigger job "publish" started pipeline 30 in group/secret (project 8).
// Downstream pipelines are asked for by the ID of their project.
const (
	e2ePipeline    = "projects/6/pipelines/20"
	secretPipeline = "projects/8/pipelines/30"
)

// triggered answers for that tree. e2eStatus is asked each time the
// trigger jobs of pipeline 7 are read: they give the status of pipeline 20.
func triggered(e2eStatus func() string) func(path string) (string, int) {
	return func(path string) (string, int) {
		switch path {
		case waitMR:
			return `{"iid":1,"project_id":5,"sha":"aaa111","head_pipeline":{"id":7,"project_id":5,"sha":"aaa111","ref":"refs/merge-requests/1/head","status":"success"}}`, 200
		case waitPipeline:
			return `{"id":7,"project_id":5,"sha":"aaa111","status":"success","web_url":"https://gitlab.com/group/project/-/pipelines/7"}`, 200
		case waitPipeline + "/jobs":
			return `[]`, 200
		case waitPipeline + "/bridges":
			status := e2eStatus()
			return `[{"id":71,"name":"e2e","status":"` + status + `","downstream_pipeline":{"id":20,"project_id":6,"status":"` + status + `","web_url":"https://gitlab.com/group/e2e/-/pipelines/20"}}]`, 200
		case e2ePipeline + "/jobs":
			return `[{"id":200,"name":"e2e-1","stage":"test","status":"failed"}]`, 200
		case e2ePipeline + "/bridges":
			return `[{"id":21,"name":"publish","status":"success","downstream_pipeline":{"id":30,"project_id":8,"status":"success","web_url":"https://gitlab.com/group/secret/-/pipelines/30"}}]`, 200
		case "projects/6/jobs/200/trace":
			return "$ make e2e\nFAIL: TestE2E\n", 200
		case secretPipeline + "/jobs", secretPipeline + "/bridges":
			return `[]`, 200
		}
		return `{"message":"404 Not found"}`, 404
	}
}

// refuse answers status for each of paths.
func refuse(status int, paths ...string) map[string]func() (string, int) {
	body := map[int]string{403: `{"message":"403 Forbidden"}`, 404: `{"message":"404 Project Not Found"}`}[status]
	except := map[string]func() (string, int){}
	for _, path := range paths {
		except[path] = func() (string, int) { return body, status }
	}
	return except
}

// downstreamResult is what the tests read of a result.
type downstreamResult struct {
	Downstream []struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
		Bridge string `json:"bridge"`
		Parent int    `json:"parent"`
	} `json:"downstream"`
	FailedJobs []struct {
		Name string `json:"name"`
		Log  string `json:"log"`
	} `json:"failed_jobs"`
	Running  bool     `json:"running"`
	TimedOut bool     `json:"timed_out"`
	Warnings []string `json:"warnings"`
}

func (e *waitEnv) downstreamResult(t *testing.T) downstreamResult {
	t.Helper()
	var res downstreamResult
	if err := json.Unmarshal(e.stdout.Bytes(), &res); err != nil {
		t.Fatalf("%v: %s", err, e.stdout.String())
	}
	return res
}

// A downstream pipeline that the token cannot read does not fail the
// commands: it is listed with the status its trigger job reports, and a
// warning says what is missing.
func TestPipelineWithADownstreamPipelineTheTokenCannotRead(t *testing.T) {
	const pipeline20 = `the downstream pipeline 20 (in group/e2e, started by the trigger job "e2e" of pipeline 7)`
	for _, tt := range []struct {
		name       string
		refused    map[string]func() (string, int)
		downstream []int
		jobs       []string
		warning    []string
		not        []string
	}{
		{"its trigger jobs are refused", refuse(403, e2ePipeline+"/bridges"), []int{20}, []string{"e2e-1"},
			[]string{pipeline20, "HTTP 403", "Its status (failed)", "its own downstream pipelines"}, []string{"its failed jobs", "It failed"}},
		{"its jobs are refused", refuse(403, e2ePipeline+"/jobs"), []int{20, 30}, nil,
			[]string{pipeline20, "HTTP 403", "Its status (failed)", "its failed jobs", "It failed", "https://gitlab.com/group/e2e/-/pipelines/20"}, []string{"its own downstream pipelines"}},
		{"its project is not visible", refuse(404, e2ePipeline+"/jobs", e2ePipeline+"/bridges"), []int{20}, nil,
			[]string{pipeline20, "HTTP 404", "Its status (failed)", "its failed jobs; its own downstream pipelines", "It failed"}, nil},
		{"second level", refuse(404, secretPipeline+"/jobs", secretPipeline+"/bridges"), []int{20, 30}, []string{"e2e-1"},
			[]string{`the downstream pipeline 30 (in group/secret, started by the trigger job "publish" of pipeline 20)`, "HTTP 404", "Its status (success)", "its failed jobs; its own downstream pipelines"}, []string{"It failed"}},
	} {
		for _, args := range [][]string{
			{"pipeline", "get", "group/project!1"},
			{"pipeline", "wait", "group/project!1", "--interval", "5s"},
		} {
			t.Run(args[1]+", "+tt.name, func(t *testing.T) {
				e := newWaitEnv(t, with(triggered(always("failed")), tt.refused))
				code, _ := e.run(t, args...)
				if code != 0 {
					t.Fatalf("exit %d (stdout: %s, stderr: %s)", code, e.stdout.String(), e.stderr.String())
				}
				line := e.stdout.String()
				res := e.downstreamResult(t)
				var ids []int
				for _, d := range res.Downstream {
					ids = append(ids, d.ID)
				}
				if !slices.Equal(ids, tt.downstream) || res.Downstream[0].Status != "failed" || res.Downstream[0].Bridge != "e2e" || res.Downstream[0].Parent != 7 {
					t.Errorf("want the downstream pipelines %v, the first failed, got %s", tt.downstream, line)
				}
				var jobs []string
				for _, j := range res.FailedJobs {
					jobs = append(jobs, j.Name)
					if j.Log == "" {
						t.Errorf("want the log of %s, got %s", j.Name, line)
					}
				}
				if !slices.Equal(jobs, tt.jobs) || res.Running || res.TimedOut {
					t.Errorf("want the failed jobs %v of a finished tree, got %s", tt.jobs, line)
				}
				if len(res.Warnings) != 1 {
					t.Fatalf("want one warning, got %s", line)
				}
				for _, want := range tt.warning {
					if !strings.Contains(res.Warnings[0], want) {
						t.Errorf("the warning lacks %q: %s", want, res.Warnings[0])
					}
				}
				for _, not := range tt.not {
					if strings.Contains(res.Warnings[0], not) {
						t.Errorf("the warning must not have %q: %s", not, res.Warnings[0])
					}
				}
			})
		}
	}
}

// The wait ends when the trigger job reports that the downstream pipeline
// it cannot read has finished: not before, and not never. The warning is
// printed once, whatever the number of polls.
func TestPipelineWaitEndsWhenTheUnreadableDownstreamPipelineFinishes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		refused map[string]func() (string, int)
	}{
		{"its trigger jobs are refused", refuse(403, e2ePipeline+"/bridges")},
		{"its jobs are refused", refuse(403, e2ePipeline+"/jobs")},
		{"its project is not visible", refuse(404, e2ePipeline+"/jobs", e2ePipeline+"/bridges")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const polls = 5
			read := 0
			e := newWaitEnv(t, with(triggered(func() string {
				if read++; read <= polls {
					return "running"
				}
				return "failed"
			}), tt.refused))
			code, _ := e.wait(t)
			if code != 0 {
				t.Fatalf("exit %d (stdout: %s, stderr: %s)", code, e.stdout.String(), e.stderr.String())
			}
			res := e.downstreamResult(t)
			if len(res.Downstream) == 0 || res.Downstream[0].ID != 20 || res.Downstream[0].Status != "failed" || res.Running || res.TimedOut {
				t.Errorf("want the downstream pipeline 20, failed, in a finished tree, got %s", e.stdout.String())
			}
			if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "the downstream pipeline 20") || !strings.Contains(res.Warnings[0], "Its status (failed)") {
				t.Errorf("want one warning about the pipeline 20, got %s", e.stdout.String())
			}
			// The polls while it ran, then the two reads of the end.
			if n := e.count(waitPipeline + "/bridges"); n != polls+2 {
				t.Errorf("the trigger jobs of the pipeline 7 were read %d times, want %d: once for each poll", n, polls+2)
			}
			if e.stderr.Len() != 0 {
				t.Errorf("stderr: %s", e.stderr.String())
			}
		})
	}
}

// --timeout while the unreadable downstream pipeline still runs.
func TestPipelineWaitTimeoutWithAnUnreadableDownstreamPipeline(t *testing.T) {
	e := newWaitEnv(t, with(triggered(always("running")), refuse(404, e2ePipeline+"/jobs", e2ePipeline+"/bridges")))
	code, _ := e.wait(t, "--timeout", "2m")
	if code != 0 {
		t.Fatalf("exit %d (stdout: %s, stderr: %s)", code, e.stdout.String(), e.stderr.String())
	}
	res := e.downstreamResult(t)
	if len(res.Downstream) != 1 || res.Downstream[0].Status != "running" || !res.Running || !res.TimedOut {
		t.Errorf("want the downstream pipeline 20, running, and timed_out, got %s", e.stdout.String())
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "Its status (running)") || strings.Contains(res.Warnings[0], "It failed") {
		t.Errorf("want one warning about the pipeline 20, got %s", e.stdout.String())
	}
}

// GitLab failing (500) on the same endpoints is not a refusal: "pipeline
// wait" tries again and prints the whole tree, "pipeline get" fails.
func TestPipelineDownstreamOutageIsNotARefusal(t *testing.T) {
	for _, path := range []string{e2ePipeline + "/bridges", e2ePipeline + "/jobs", secretPipeline + "/bridges"} {
		t.Run("wait, "+path, func(t *testing.T) {
			asked := 0
			answers := triggered(always("failed"))
			e := newWaitEnv(t, with(answers, map[string]func() (string, int){
				path: func() (string, int) {
					if asked++; asked <= 2 {
						return outage, 500
					}
					return answers(path)
				},
			}))
			code, _ := e.wait(t)
			if code != 0 {
				t.Fatalf("exit %d (stdout: %s, stderr: %s)", code, e.stdout.String(), e.stderr.String())
			}
			res := e.downstreamResult(t)
			if len(res.Downstream) != 2 || len(res.FailedJobs) != 1 || res.FailedJobs[0].Name != "e2e-1" || len(res.Warnings) != 0 || res.Running {
				t.Errorf("want the whole tree, its failed job and no warning, got %s", e.stdout.String())
			}
			// Trigger jobs are read twice at the end: by the last poll,
			// then with the jobs.
			if asked < 3 || !strings.Contains(e.stderr.String(), "cannot reach GitLab") {
				t.Errorf("want 2 failures then the retries that work, got %d requests (stderr: %s)", asked, e.stderr.String())
			}
		})
		t.Run("get, "+path, func(t *testing.T) {
			e := newWaitEnv(t, with(triggered(always("failed")), map[string]func() (string, int){
				path: func() (string, int) { return outage, 500 },
			}))
			if code, _ := e.run(t, "pipeline", "get", "group/project!1"); code != 1 || e.stdout.Len() != 0 || !strings.Contains(e.stderr.String(), "HTTP 500") {
				t.Errorf("exit %d, want 1 and the error; stdout: %s, stderr: %s", code, e.stdout.String(), e.stderr.String())
			}
		})
	}
}

// GitLab refusing the pipeline asked for itself is an error, as before.
func TestPipelineRefusedRootIsAnError(t *testing.T) {
	for _, path := range []string{waitPipeline, waitPipeline + "/jobs", waitPipeline + "/bridges"} {
		for _, args := range [][]string{
			{"pipeline", "get", "group/project!1"},
			{"pipeline", "wait", "group/project!1", "--interval", "5s"},
		} {
			e := newWaitEnv(t, with(triggered(always("failed")), refuse(403, path)))
			if code, _ := e.run(t, args...); code != 1 || e.stdout.Len() != 0 || !strings.Contains(e.stderr.String(), "HTTP 403") {
				t.Errorf("%s refused, %s: exit %d, want 1 and the error; stdout: %s, stderr: %s", path, args[1], code, e.stdout.String(), e.stderr.String())
			}
		}
	}
}
