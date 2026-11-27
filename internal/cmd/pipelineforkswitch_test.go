package cmd

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// switching is a merge request from a fork whose head commit gets a
// second pipeline during the wait: pipeline 7 runs first, and from poll 3
// on the merge request reports pipeline 9, which a member of the parent
// project ran there (or the other way round: 7 in the parent project,
// then 9 in the fork). Both fail a job, each in its own project.
type switching struct {
	// first and then are the IDs of the projects of pipelines 7 and 9:
	// 1 is the parent project (group/project), 2 the fork.
	first, then int
	poll        int
}

// api is how the pipelines of a project are asked for: the fork by its
// ID, the project of the merge request as it was named.
func (s *switching) api(project int) string {
	if project == 2 {
		return "projects/2"
	}
	return "projects/group/project"
}

func (s *switching) answers(path string) (string, int) {
	if path == waitMR {
		s.poll++
		id, project := 7, s.first
		if s.poll >= 3 {
			id, project = 9, s.then
		}
		status := "running"
		if s.poll >= 6 {
			status = "failed"
		}
		return `{"iid":1,"project_id":1,"sha":"` + forkHeadSHA + `","head_pipeline":` + forkPipelineRef(id, project, forkHeadSHA, status, true) + `}`, 200
	}
	for id, project := range map[int]int{7: s.first, 9: s.then} {
		base, n := s.api(project), strconv.Itoa(id)
		switch path {
		case base + "/pipelines/" + n:
			status := "running"
			if s.poll >= 6 {
				status = "failed"
			}
			return forkPipelineRef(id, project, forkHeadSHA, status, true), 200
		case base + "/pipelines/" + n + "/jobs":
			return `[{"id":` + n + `0,"name":"test-` + n + `","stage":"test","status":"failed"}]`, 200
		case base + "/pipelines/" + n + "/bridges":
			return `[]`, 200
		case base + "/jobs/" + n + "0/trace":
			return "FAIL in pipeline " + n + "\n", 200
		}
	}
	// Among others: a pipeline asked of the project it is not in.
	return `{"message":"404 Not found"}`, 404
}

// The project is chosen again at each poll: when the merge request gets
// a new pipeline for the same commit in the other project, that pipeline
// is read from its own project, not from the project of the one before.
func TestPipelineWaitFollowsThePipelineToItsProject(t *testing.T) {
	for _, tt := range []struct {
		name        string
		first, then int
		project     string
	}{
		{"from the fork to the parent project", 2, 1, "group/project"},
		{"from the parent project to the fork", 1, 2, "contributor/project"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &switching{first: tt.first, then: tt.then}
			e := newWaitEnv(t, s.answers)
			code, out := e.wait(t)
			if code != 0 {
				t.Fatalf("exit %d (stdout: %s, stderr: %s)", code, e.stdout.String(), e.stderr.String())
			}
			line := e.stdout.String()
			id, status, warnings, _ := printed(t, e, out)
			var p struct{ Project string }
			if err := json.Unmarshal(out["pipeline"], &p); err != nil || id != 9 || status != "failed" || p.Project != tt.project || len(warnings) != 0 || strings.Contains(line, "unreadable") {
				t.Errorf("want the pipeline 9 of %s, failed, read in full, got %s", tt.project, line)
			}
			var jobs []struct{ Name, Project, Log string }
			if err := json.Unmarshal(out["failed_jobs"], &jobs); err != nil || len(jobs) != 1 || jobs[0].Name != "test-9" || jobs[0].Project != tt.project || jobs[0].Log != "FAIL in pipeline 9" {
				t.Errorf("want the failed job of the pipeline 9, in %s, with its log, got %s", tt.project, line)
			}
			// Each pipeline was read, from its own project only.
			first, then := s.api(tt.first), s.api(tt.then)
			if e.count(first+"/pipelines/7") == 0 || e.count(then+"/pipelines/9") == 0 || e.count(then+"/jobs/90/trace") != 1 {
				t.Errorf("want the pipeline 7 read from %s and the pipeline 9 from %s: %v", first, then, e.seen)
			}
			for _, wrong := range []string{then + "/pipelines/7", first + "/pipelines/9", first + "/pipelines/9/jobs", first + "/pipelines/9/bridges", first + "/jobs/90/trace"} {
				if n := e.count(wrong); n != 0 {
					t.Errorf("%s was asked for %d times: the pipeline is not in that project", wrong, n)
				}
			}
			if want := "glcli: now waiting for pipeline 9\n"; e.stderr.String() != want {
				t.Errorf("stderr: %q, want %q", e.stderr.String(), want)
			}
		})
	}
}
