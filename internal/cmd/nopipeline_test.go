package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/pipeline"
)

func printPipelineResult(t *testing.T, res pipelineResult) (string, map[string]json.RawMessage) {
	t.Helper()
	var out, errb bytes.Buffer
	rt := cli.New(&out, &errb)
	if err := rt.Print(res, "failed_jobs"); err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	return out.String(), got
}

// What "pipeline get" and "pipeline wait" print while the commit of a
// merge request has no pipeline: no "pipeline" key rather than {"id":0}.
func TestNoPipelineHasNoPipelineKey(t *testing.T) {
	for _, res := range []pipelineResult{
		{Report: &pipeline.Report{Running: true}, SHA: "abc"},
		{Report: &pipeline.Report{Running: true}, SHA: "abc", TimedOut: true},
	} {
		line, got := printPipelineResult(t, res)
		if _, ok := got["pipeline"]; ok {
			t.Errorf("there is no pipeline, got a \"pipeline\" key: %s", line)
		}
		if string(got["running"]) != "true" || string(got["sha"]) != `"abc"` || string(got["failed_jobs"]) != "[]" {
			t.Errorf("want running, sha and an empty failed_jobs, got %s", line)
		}
		if res.TimedOut && string(got["timed_out"]) != "true" {
			t.Errorf("want timed_out, got %s", line)
		}
	}
}

func TestPipelineKeepsItsPipelineKey(t *testing.T) {
	line, got := printPipelineResult(t, pipelineResult{Report: &pipeline.Report{
		Pipeline: pipeline.Summary{ID: 456, Project: "group/project", Status: "success", URL: "https://gitlab.com/group/project/-/pipelines/456"},
	}})
	var p pipeline.Summary
	if err := json.Unmarshal(got["pipeline"], &p); err != nil || p.ID != 456 || p.Status != "success" {
		t.Errorf("want the pipeline 456, got %s", line)
	}
	if string(got["running"]) != "false" || string(got["failed_jobs"]) != "[]" {
		t.Errorf("want running false and an empty failed_jobs, got %s", line)
	}
}

// The whole command, on a merge request whose head commit has no pipeline.
func TestPipelineGetWithoutPipeline(t *testing.T) {
	rt, stdout, stderr := fakeGitLab(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/merge_requests/1"):
			fmt.Fprint(w, `{"id":10,"iid":1,"project_id":5,"sha":"abc"}`)
		case strings.HasSuffix(r.URL.Path, "/merge_requests/1/pipelines"):
			fmt.Fprint(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	})
	if code := run(rt, []string{"pipeline", "get", "group/project!1"}); code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, stdout.String())
	}
	if _, ok := got["pipeline"]; ok {
		t.Errorf("there is no pipeline, got a \"pipeline\" key: %s", stdout.String())
	}
	if string(got["running"]) != "true" || string(got["sha"]) != `"abc"` || string(got["failed_jobs"]) != "[]" || len(got["hints"]) == 0 {
		t.Errorf("want running, sha, an empty failed_jobs and a hint, got %s", stdout.String())
	}
}
