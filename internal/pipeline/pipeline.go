// Package pipeline reports on a pipeline and its downstream pipelines:
// their status, and the failed jobs with the end of their log.
package pipeline

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/maelvls/glcli/internal/gitlab"
	"github.com/maelvls/glcli/internal/mr"
	"github.com/maelvls/glcli/internal/ref"
)

// Summary is one pipeline.
type Summary struct {
	ID      int    `json:"id"`
	Project string `json:"project"`
	Status  string `json:"status"`
	URL     string `json:"url"`
	SHA     string `json:"sha,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Source  string `json:"source,omitempty"`
	// Bridge is the trigger job that started a downstream pipeline.
	Bridge string `json:"bridge,omitempty"`
	// Parent is the ID of the pipeline that triggered this one.
	Parent int `json:"parent,omitempty"`
	Depth  int `json:"depth,omitempty"`
}

// FailedJob is a job that failed, in any pipeline of the tree.
type FailedJob struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Stage         string `json:"stage"`
	Status        string `json:"status"`
	URL           string `json:"url"`
	Pipeline      int    `json:"pipeline"`
	Project       string `json:"project"`
	FailureReason string `json:"failure_reason,omitempty"`
	AllowFailure  bool   `json:"allow_failure,omitempty"`
	// Log is the end of the job's log, without colours and section markers.
	Log string `json:"log,omitempty"`
}

// Report is a pipeline and its downstream pipelines.
type Report struct {
	Pipeline   Summary     `json:"pipeline"`
	Downstream []Summary   `json:"downstream,omitempty"`
	FailedJobs []FailedJob `json:"failed_jobs"`
	// Running is true while any pipeline of the tree has not finished.
	Running bool `json:"running"`
	// AllowedFailures counts the failed jobs left out because they are
	// allowed to fail (see --include-allowed).
	AllowedFailures int `json:"allowed_failures,omitempty"`
	// Truncated is true when a pipeline had more failed jobs (1000) or
	// trigger jobs (500) than were read: failed jobs may be missing.
	Truncated bool `json:"truncated,omitempty"`
	// Warnings say what could not be read, such as the log of a failed
	// job. They are for the caller to print: not part of the JSON.
	Warnings []string `json:"-"`
}

// Options tune the report.
type Options struct {
	LogLines       int
	IncludeAllowed bool
	// Downstream follows trigger jobs into downstream pipelines.
	Downstream bool
	MaxDepth   int
	// StatusOnly reads no jobs, only the status of each pipeline: what
	// "pipeline wait" needs at each poll.
	StatusOnly bool
}

// Collect reports on pipeline id of project (a path or numeric ID).
func Collect(ctx context.Context, c *gitlab.Client, project string, id int, o Options) (*Report, error) {
	var p gitlab.Pipeline
	if err := c.Get(ctx, gitlab.Project(project)+"/pipelines/"+strconv.Itoa(id), nil, &p); err != nil {
		return nil, err
	}
	r := &Report{Pipeline: summary(p.PipelineRef, project)}
	if err := walk(ctx, c, r, project, p.ID, 0, o); err != nil {
		return nil, err
	}
	r.Running = r.Running || !mr.Finished(p.Status)
	if o.LogLines > 0 {
		r.Warnings = append(r.Warnings, addLogs(ctx, c, r.FailedJobs, o.LogLines)...)
	}
	return r, nil
}

func summary(p gitlab.PipelineRef, project string) Summary {
	if fromURL := projectFromURL(p.WebURL); fromURL != "" {
		project = fromURL
	}
	return Summary{ID: p.ID, Project: project, Status: p.Status, URL: p.WebURL, SHA: p.SHA, Ref: p.Ref, Source: p.Source}
}

func projectFromURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	p, _, ok := strings.Cut(strings.Trim(parsed.Path, "/"), "/-/")
	if !ok {
		return ""
	}
	return p
}

func walk(ctx context.Context, c *gitlab.Client, r *Report, project string, id, depth int, o Options) error {
	base := gitlab.Project(project) + "/pipelines/" + strconv.Itoa(id)
	if !o.StatusOnly {
		if err := failedJobs(ctx, c, r, project, id, o); err != nil {
			return err
		}
	}
	if !o.Downstream || depth >= o.MaxDepth {
		return nil
	}
	bridges, truncated, err := gitlab.GetAll[gitlab.Bridge](ctx, c, base+"/bridges", nil, 5)
	if err != nil {
		return err
	}
	r.Truncated = r.Truncated || truncated
	for _, b := range bridges {
		if b.DownstreamPipeline == nil {
			if !mr.Finished(b.Status) && b.Status != "created" {
				r.Running = true
			}
			continue
		}
		dp := b.DownstreamPipeline
		dproject := projectFromURL(dp.WebURL)
		if dproject == "" {
			dproject = strconv.Itoa(dp.ProjectID)
		}
		s := summary(*dp, dproject)
		s.Bridge, s.Parent, s.Depth = b.Name, id, depth+1
		r.Downstream = append(r.Downstream, s)
		if !mr.Finished(dp.Status) {
			r.Running = true
		}
		if err := walk(ctx, c, r, dproject, dp.ID, depth+1, o); err != nil {
			return err
		}
	}
	return nil
}

// failedJobs adds the failed jobs of one pipeline to r. GitLab is asked
// for these only (scope[]=failed): a pipeline can have a thousand jobs. A
// job that failed and was retried is not among them: without
// include_retried, GitLab lists the latest run of each job.
func failedJobs(ctx context.Context, c *gitlab.Client, r *Report, project string, id int, o Options) error {
	path := gitlab.Project(project) + "/pipelines/" + strconv.Itoa(id) + "/jobs"
	jobs, truncated, err := gitlab.GetAll[gitlab.Job](ctx, c, path, url.Values{"scope[]": {"failed"}}, 10)
	if err != nil {
		return err
	}
	r.Truncated = r.Truncated || truncated
	for _, j := range jobs {
		if j.Status != "failed" {
			continue
		}
		if j.AllowFailure && !o.IncludeAllowed {
			r.AllowedFailures++
			continue
		}
		r.FailedJobs = append(r.FailedJobs, FailedJob{
			ID: j.ID, Name: j.Name, Stage: j.Stage, Status: j.Status, URL: j.WebURL,
			Pipeline: id, Project: project, FailureReason: j.FailureReason, AllowFailure: j.AllowFailure,
		})
	}
	return nil
}

var (
	ansi    = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	section = regexp.MustCompile(`section_(start|end):\d+:[A-Za-z0-9_.\-\[\]=,]+\r?`)
	// stepScriptEnd ends the output of the job's script; what follows is
	// after_script, artifact upload and clean-up.
	stepScriptEnd = regexp.MustCompile(`section_end:\d+:step_script`)
	// timestamp is the prefix runners with FF_TIMESTAMPS put on each line:
	// "2026-11-24T06:40:33.988563Z 01E " (stream ID, O or E, + for a
	// continued line).
	timestamp = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z [0-9a-fA-F]{2}[OE]\+? ?`)
)

// CleanLog returns the last lines of a job log's script output: colours,
// section markers, runner timestamps, progress-bar overwrites (\r) and
// blank lines removed. What runs after the script (after_script, artifact
// upload, clean-up) is left out, except GitLab's final "ERROR: Job
// failed" line.
func CleanLog(raw string, lines int) string {
	var verdict string
	if loc := stepScriptEnd.FindAllStringIndex(raw, -1); len(loc) > 0 {
		last := loc[len(loc)-1]
		for _, l := range cleanLines(raw[last[1]:]) {
			if strings.HasPrefix(l, "ERROR: Job failed") {
				verdict = l
			}
		}
		raw = raw[:last[0]]
	}
	out := cleanLines(raw)
	if len(out) > lines {
		out = out[len(out)-lines:]
	}
	if verdict != "" {
		out = append(out, verdict)
	}
	return strings.Join(out, "\n")
}

func cleanLines(raw string) []string {
	raw = section.ReplaceAllString(raw, "")
	raw = ansi.ReplaceAllString(raw, "")
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		l = strings.TrimRight(l, "\r")
		if i := strings.LastIndex(l, "\r"); i >= 0 {
			l = l[i+1:]
		}
		l = timestamp.ReplaceAllString(l, "")
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// addLogs reads the end of the log of each job, four at a time. A log
// that cannot be read (a downstream project the token cannot read, an
// erased job, a log too long to download in time) leaves the job without
// "log": it returns one warning for each, in the order of the jobs.
func addLogs(ctx context.Context, c *gitlab.Client, jobs []FailedJob, lines int) (warnings []string) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	// Each goroutine writes its own element of jobs and errs.
	errs := make([]error, len(jobs))
	for i := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// Lines are rarely longer than 300 bytes; read a generous tail.
			raw, err := c.GetText(ctx, gitlab.Project(jobs[i].Project)+"/jobs/"+strconv.Itoa(jobs[i].ID)+"/trace", int64(lines)*400+32*1024)
			if err != nil {
				errs[i] = err
				return
			}
			jobs[i].Log = CleanLog(raw, lines)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("the log of the failed job %q (%d, in %s) could not be read: %v", jobs[i].Name, jobs[i].ID, jobs[i].Project, err))
		}
	}
	return warnings
}

// Found is what ForMR found for a commit of a merge request.
type Found struct {
	// ID is the pipeline of the commit, 0 when there is none: GitLab has
	// not created it yet, or never will.
	ID int
	// SHA is the commit: the head of the merge request (in full), or
	// the SHA asked for.
	SHA string
	// Unconfirmed is the head pipeline of the merge request when ID is 0
	// and that pipeline may well be the pipeline of the head commit: it
	// runs on a merged-results or merge-train commit that cannot be read
	// (HTTP 404), so its parents could not be checked.
	Unconfirmed int
}

// ForMR finds the pipeline of a merge request for the commit sha (full or
// abbreviated; the head commit when empty): the head pipeline when it ran
// on that commit, else the newest of the last 20 pipelines of the merge
// request that did. A merged-results or merge-train pipeline runs on a
// temporary merge commit of the commit (see mr.PipelineOnCommit).
func ForMR(ctx context.Context, c *gitlab.Client, r ref.MR, sha string) (Found, error) {
	var m gitlab.MergeRequest
	if err := c.Get(ctx, mr.Base(r), nil, &m); err != nil {
		return Found{}, err
	}
	head := sha == "" || strings.HasPrefix(m.SHA, sha)
	if head {
		sha = m.SHA
	}
	found := Found{SHA: sha}
	unconfirmed := 0
	if hp := m.HeadPipeline; hp != nil {
		on, err := mr.PipelineOnCommit(ctx, c, m, *hp, sha)
		if err != nil {
			return found, err
		}
		if on == mr.OnCommitYes {
			found.ID = hp.ID
			return found, nil
		}
		if on == mr.OnCommitUnknown && head {
			unconfirmed = hp.ID
		}
	}
	var list []gitlab.PipelineRef
	if err := c.Get(ctx, mr.Base(r)+"/pipelines", url.Values{"per_page": {"20"}}, &list); err != nil {
		return found, err
	}
	for _, p := range list {
		// GitLab lists the newest first: the pipelines older than one
		// that matches are not looked at, which spares their commits.
		if p.ID <= found.ID {
			continue
		}
		on, err := mr.PipelineOnCommit(ctx, c, m, p, sha)
		if err != nil {
			return found, err
		}
		if on == mr.OnCommitYes {
			found.ID = p.ID
		}
	}
	if found.ID == 0 {
		found.Unconfirmed = unconfirmed
	}
	return found, nil
}
