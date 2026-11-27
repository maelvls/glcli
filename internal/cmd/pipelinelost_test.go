package cmd

import (
	"strings"
	"testing"
)

// lostTree is the fake GitLab of the tests of this file: the head
// pipeline (7, success) of group/project!1 started pipeline 20 in
// group/e2e (project 6, trigger job "e2e"), which started pipeline 30 in
// group/secret (project 8, trigger job "publish"). The token can read all
// of it, until the fake refuses something.
type lostTree struct {
	// fork puts pipeline 7 in the fork the merge request comes from,
	// contributor/project (project 2).
	fork bool
	// status20 and status30 give the status of the downstream pipelines
	// at each poll (the first is 1).
	status20, status30 func(poll int) string
	// refuse returns the HTTP status to answer path with, 0 to answer it
	// for real. nth counts the requests for that path, from 1.
	refuse func(path string, poll, nth int) int
	// late adds, in the nth answer for the trigger jobs of pipeline 7, a
	// trigger job that started pipeline 40 in a project the token cannot
	// read, and says that everything has finished.
	late func(nth int) bool

	poll int
	nth  map[string]int
}

// root is the API path of pipeline 7.
func (l *lostTree) root() string {
	if l.fork {
		return "projects/2/pipelines/7"
	}
	return waitPipeline
}

func (l *lostTree) answers(path string) (string, int) {
	if l.nth == nil {
		l.nth = map[string]int{}
	}
	l.nth[path]++
	if path == waitMR {
		// One read of the merge request for each poll.
		l.poll++
	}
	if l.refuse != nil {
		if status := l.refuse(path, l.poll, l.nth[path]); status != 0 {
			return map[int]string{403: `{"message":"403 Forbidden"}`, 404: `{"message":"404 Project Not Found"}`}[status], status
		}
	}
	project, web := "5", "group/project"
	if l.fork {
		project, web = "2", "contributor/project"
	}
	status := func(f func(int) string) string {
		if f == nil {
			return "success"
		}
		return f(l.poll)
	}
	switch path {
	case waitMR:
		return `{"iid":1,"project_id":5,"sha":"aaa111","head_pipeline":{"id":7,"project_id":` + project + `,"sha":"aaa111","ref":"feature","status":"success","web_url":"https://gitlab.com/` + web + `/-/pipelines/7"}}`, 200
	case l.root():
		return `{"id":7,"project_id":` + project + `,"sha":"aaa111","status":"success","web_url":"https://gitlab.com/` + web + `/-/pipelines/7"}`, 200
	case l.root() + "/jobs":
		return `[]`, 200
	case l.root() + "/bridges":
		if l.late != nil && l.late(l.nth[path]) {
			return `[{"id":71,"name":"e2e","status":"success","downstream_pipeline":{"id":20,"project_id":6,"status":"success","web_url":"https://gitlab.com/group/e2e/-/pipelines/20"}},
				{"id":72,"name":"late","status":"success","downstream_pipeline":{"id":40,"project_id":9,"status":"success","web_url":"https://gitlab.com/group/hidden/-/pipelines/40"}}]`, 200
		}
		s := status(l.status20)
		return `[{"id":71,"name":"e2e","status":"` + s + `","downstream_pipeline":{"id":20,"project_id":6,"status":"` + s + `","web_url":"https://gitlab.com/group/e2e/-/pipelines/20"}}]`, 200
	case e2ePipeline + "/jobs":
		return `[{"id":200,"name":"e2e-1","stage":"test","status":"failed"}]`, 200
	case e2ePipeline + "/bridges":
		s := status(l.status30)
		if l.late != nil && l.late(l.nth[l.root()+"/bridges"]) {
			s = "success"
		}
		return `[{"id":21,"name":"publish","status":"` + s + `","downstream_pipeline":{"id":30,"project_id":8,"status":"` + s + `","web_url":"https://gitlab.com/group/secret/-/pipelines/30"}}]`, 200
	case "projects/6/jobs/200/trace":
		return "$ make e2e\nFAIL: TestE2E\n", 200
	case secretPipeline + "/jobs", secretPipeline + "/bridges":
		return `[]`, 200
	}
	return `{"message":"404 Not found"}`, 404
}

// until gives one status up to a poll, and another after it.
func until(poll int, before, after string) func(int) string {
	return func(now int) string {
		if now <= poll {
			return before
		}
		return after
	}
}

// lost checks that the wait ended on an error at the poll given: nothing
// on stdout, exit 1, and a message that names the pipeline, says that it
// could be read earlier, and gives GitLab's answer.
func lost(t *testing.T, e *waitEnv, l *lostTree, code, poll int, name, status string) {
	t.Helper()
	if code != 1 || e.stdout.Len() != 0 {
		t.Fatalf("exit %d, want 1 and nothing on stdout; stdout: %s, stderr: %s", code, e.stdout.String(), e.stderr.String())
	}
	for _, want := range []string{name + " could be read earlier in this wait and no longer can be", status} {
		if !strings.Contains(e.stderr.String(), want) {
			t.Errorf("stderr lacks %q: %s", want, e.stderr.String())
		}
	}
	if l.poll != poll {
		t.Errorf("the wait ended at poll %d, want %d", l.poll, poll)
	}
}

// The fork of the merge request can be read, then answers 404 at one
// poll, while a downstream pipeline still runs: the wait must not end
// there on the status of pipeline 7 alone, as if the fork had never been
// readable.
func TestPipelineWaitForkRefusedDuringTheWait(t *testing.T) {
	l := &lostTree{fork: true, status20: until(8, "running", "failed"),
		refuse: func(path string, poll, _ int) int {
			if poll == 3 && strings.HasPrefix(path, "projects/2/") {
				return 404
			}
			return 0
		}}
	e := newWaitEnv(t, l.answers)
	code, _ := e.wait(t)
	lost(t, e, l, code, 3, "pipeline 7 (in contributor/project)", "HTTP 404")
}

// A downstream pipeline whose own downstream pipeline still runs stops
// being readable: its project answers 404 from poll 3 on, when its
// trigger job says that it succeeded. Or one stray 403 for its trigger
// jobs. The wait must not end without pipeline 30.
func TestPipelineWaitDownstreamRefusedDuringTheWait(t *testing.T) {
	for _, tt := range []struct {
		name   string
		refuse func(path string, poll, nth int) int
		status string
	}{
		{"its project answers 404 from then on", func(path string, poll, _ int) int {
			if poll >= 3 && strings.HasPrefix(path, "projects/6/") {
				return 404
			}
			return 0
		}, "HTTP 404"},
		{"one stray 403 for its trigger jobs", func(path string, poll, _ int) int {
			if poll == 3 && path == e2ePipeline+"/bridges" {
				return 403
			}
			return 0
		}, "HTTP 403"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := &lostTree{status20: until(2, "running", "success"), status30: until(8, "running", "failed"), refuse: tt.refuse}
			e := newWaitEnv(t, l.answers)
			code, _ := e.wait(t)
			lost(t, e, l, code, 3, "the downstream pipeline 20 (in group/e2e)", tt.status)
		})
	}
}

// The same refusals when they come with the read made at --timeout: the
// polls saw the tree running, so the result is timed out, with what the
// last poll saw.
func TestPipelineWaitRefusedAtTheTimeout(t *testing.T) {
	for _, tt := range []struct {
		name    string
		fork    bool
		refused string // API path prefix
		warning string
	}{
		{"the fork", true, "projects/2/", "pipeline 7 (in contributor/project) could be read earlier in this wait and no longer can be"},
		{"a downstream pipeline", false, "projects/6/", "the downstream pipeline 20 (in group/e2e) could be read earlier in this wait and no longer can be"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			running := func(int) string { return "running" }
			// A first wait, to learn how many times pipeline 7 is read:
			// the last time is the read made at the timeout.
			l := &lostTree{fork: tt.fork, status20: running, status30: running}
			e := newWaitEnv(t, l.answers)
			if code, out := e.wait(t, "--timeout", "2m"); code != 0 || string(out["timed_out"]) != "true" {
				t.Fatalf("exit %d, %s (stderr: %s)", code, e.stdout.String(), e.stderr.String())
			}
			last := l.nth[l.root()]

			l = &lostTree{fork: tt.fork, status20: running, status30: running}
			l.refuse = func(path string, _, _ int) int {
				if l.nth[l.root()] == last && strings.HasPrefix(path, tt.refused) {
					return 404
				}
				return 0
			}
			e = newWaitEnv(t, l.answers)
			code, out := e.wait(t, "--timeout", "2m")
			if code != 0 {
				t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
			}
			res := e.downstreamResult(t)
			id, status, warnings, hints := printed(t, e, out)
			if id != 7 || status != "success" || !res.TimedOut || !res.Running || len(hints) != 1 {
				t.Errorf("want the pipeline 7, running, timed out, with a hint, got %s", e.stdout.String())
			}
			if len(res.Downstream) != 2 || res.Downstream[0].Status != "running" || res.Downstream[1].ID != 30 {
				t.Errorf("want what the last poll saw, the pipelines 20 and 30, got %s", e.stdout.String())
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], tt.warning) || !strings.Contains(warnings[0], "HTTP 404") || strings.Contains(e.stdout.String(), "unreadable") {
				t.Errorf("want one warning that the pipeline can no longer be read, and no \"unreadable\", got %s", e.stdout.String())
			}
		})
	}
}

// At the read made at --timeout, the tree has a new pipeline that cannot
// be read, and what can be read has finished. The polls read a whole
// tree that was running: a read that lacks part of it does not make the
// result a finished one.
func TestPipelineWaitPartialReadAtTheTimeout(t *testing.T) {
	running := func(int) string { return "running" }
	l := &lostTree{status20: running, status30: running}
	e := newWaitEnv(t, l.answers)
	if code, out := e.wait(t, "--timeout", "2m"); code != 0 || string(out["timed_out"]) != "true" {
		t.Fatalf("exit %d, %s (stderr: %s)", code, e.stdout.String(), e.stderr.String())
	}
	last := l.nth[waitPipeline+"/bridges"]

	l = &lostTree{status20: running, status30: running, late: func(nth int) bool { return nth == last }}
	e = newWaitEnv(t, l.answers)
	code, out := e.wait(t, "--timeout", "2m")
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	if l.nth["projects/9/pipelines/40/bridges"] == 0 {
		t.Fatal("the test must have met the pipeline 40")
	}
	res := e.downstreamResult(t)
	_, _, warnings, hints := printed(t, e, out)
	if !res.TimedOut || !res.Running || len(res.Downstream) != 2 || res.Downstream[0].Status != "running" {
		t.Errorf("want what the last poll saw, running and timed out, got %s", e.stdout.String())
	}
	if len(hints) != 1 || !strings.Contains(hints[0], "still running at the last poll") {
		t.Errorf("want a hint that part of the tree could not be read at the end, got %s", e.stdout.String())
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "the downstream pipeline 40 (in group/hidden") {
		t.Errorf("want the warning about the pipeline 40, got %s", e.stdout.String())
	}
}

// What the token could never read in this wait is not lost: the wait does
// without it from the first poll to the last, as "pipeline get" does.
func TestPipelineWaitNeverReadableIsNotAnError(t *testing.T) {
	l := &lostTree{status20: until(4, "running", "failed"),
		refuse: func(path string, _, _ int) int {
			if strings.HasPrefix(path, "projects/6/") {
				return 404
			}
			return 0
		}}
	e := newWaitEnv(t, l.answers)
	code, _ := e.wait(t)
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	res := e.downstreamResult(t)
	if l.poll != 5 || len(res.Downstream) != 1 || res.Downstream[0].Status != "failed" || !res.Downstream[0].Unreadable || res.Running || len(res.Warnings) != 1 {
		t.Errorf("want the wait to end at poll 5 on the unreadable pipeline 20, failed, got poll %d and %s", l.poll, e.stdout.String())
	}
}

// The tree has finished, and the refusal comes with the read of the end,
// the one that reads the jobs: that is not a result either.
func TestPipelineWaitRefusedAtTheFinalRead(t *testing.T) {
	l := &lostTree{refuse: func(path string, _, nth int) int {
		// The poll reads the trigger jobs once; the read of the end, again.
		if path == e2ePipeline+"/bridges" && nth == 2 {
			return 403
		}
		return 0
	}}
	e := newWaitEnv(t, l.answers)
	code, _ := e.wait(t)
	lost(t, e, l, code, 1, "the downstream pipeline 20 (in group/e2e)", "HTTP 403")
}
