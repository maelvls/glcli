// Package pipeline reports on a pipeline and its downstream pipelines:
// their status, and the failed jobs with the end of their log.
package pipeline

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
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
	// Unreadable is true when GitLab refused to show the pipeline, or
	// part of it, to this token: its failed jobs, its downstream
	// pipelines or both are missing from the report, and a warning says
	// which. Its status is still known.
	Unreadable bool `json:"unreadable,omitempty"`
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
	// api names the project in API requests (see node).
	api string
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

// Partial reports whether a pipeline of the report is Unreadable.
func (r *Report) Partial() bool {
	return r.Pipeline.Unreadable || slices.ContainsFunc(r.Downstream, func(s Summary) bool { return s.Unreadable })
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
	root := node{Summary: r.Pipeline, at: -1, api: project, shown: project}
	if _, err := strconv.Atoi(project); err == nil {
		// Named by its ID, as the fork of a merge request is: the failed
		// jobs name it as the pipeline does, by its path.
		root.shown = r.Pipeline.Project
	}
	if err := walk(ctx, c, r, root, o); err != nil {
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

// node is a pipeline of the tree a report is about.
type node struct {
	Summary
	// at is its place in Report.Downstream, -1 for the pipeline the
	// report is about.
	at int
	// api names its project in API requests. For a downstream pipeline,
	// and for a pipeline in the fork of a merge request, that is the
	// numeric ID GitLab gives (project_id), not the path found in web_url,
	// which is for display: on an instance served under a relative URL
	// (https://host/gitlab/group/project/-/pipelines/1), there is more in
	// it than the path of the project.
	api string
	// shown names its project in the failed jobs.
	shown string
}

// walk adds to r the failed jobs of the pipeline n and the pipelines
// downstream of it.
//
// A downstream pipeline may be in a project the token cannot read: GitLab
// then refuses its jobs and its trigger jobs (Denied). That does not fail
// the report: the pipeline is listed with the status the trigger job that
// started it gives, which is read again at each call, with the trigger
// jobs of its parent; it is not followed further, it is marked Unreadable
// and a warning says what is missing. The same refusal for the pipeline
// the report is about (depth 0) is an error, as is any other error for
// any pipeline.
func walk(ctx context.Context, c *gitlab.Client, r *Report, n node, o Options) error {
	id, depth := n.ID, n.Depth
	base := gitlab.Project(n.api) + "/pipelines/" + strconv.Itoa(id)
	var (
		missing []string
		denied  error
	)
	// refused notes what GitLab refuses to show of a downstream pipeline.
	refused := func(err error, what string) bool {
		if depth == 0 || !Denied(err) {
			return false
		}
		missing = append(missing, what)
		if denied == nil {
			denied = err
		}
		return true
	}
	if !o.StatusOnly {
		if err := failedJobs(ctx, c, r, n, o); err != nil && !refused(err, missingJobs) {
			return err
		}
	}
	var bridges []gitlab.Bridge
	if o.Downstream && depth < o.MaxDepth {
		var (
			truncated bool
			err       error
		)
		bridges, truncated, err = gitlab.GetAll[gitlab.Bridge](ctx, c, base+"/bridges", nil, 5)
		if err != nil && !refused(err, missingDownstream) {
			return err
		}
		r.Truncated = r.Truncated || truncated
	}
	if denied != nil {
		r.Downstream[n.at].Unreadable = true
		r.Warnings = append(r.Warnings, unreadable(n.Summary, missing, denied))
	}
	for _, b := range bridges {
		if b.DownstreamPipeline == nil {
			if !mr.Finished(b.Status) && b.Status != "created" {
				r.Running = true
			}
			continue
		}
		dp := b.DownstreamPipeline
		// Shown by the path in its web_url, or else by its ID.
		s := summary(*dp, strconv.Itoa(dp.ProjectID))
		s.Bridge, s.Parent, s.Depth = b.Name, id, depth+1
		r.Downstream = append(r.Downstream, s)
		if !mr.Finished(dp.Status) {
			r.Running = true
		}
		down := node{Summary: s, at: len(r.Downstream) - 1, api: s.Project, shown: s.Project}
		if dp.ProjectID != 0 {
			down.api = strconv.Itoa(dp.ProjectID)
		}
		if err := walk(ctx, c, r, down, o); err != nil {
			return err
		}
	}
	return nil
}

// What a pipeline that cannot be read lacks in the report.
const (
	missingJobs       = "its failed jobs"
	missingDownstream = `its own downstream pipelines, which "pipeline wait" does not wait for (they may still be running, or have failed)`
)

// lacking is the end of the warning for the pipeline p, of which GitLab
// refused to show what missing lists.
func lacking(p Summary, missing []string) string {
	w := "Missing from this result: " + strings.Join(missing, "; ") + "."
	if p.Status == "failed" && slices.Contains(missing, missingJobs) {
		w += ` It failed, and "failed_jobs" does not say why`
		if p.URL != "" {
			return w + ": see " + p.URL
		}
		w += "."
	}
	return w
}

// unreadable is the warning for the downstream pipeline p, of which
// GitLab refused (err) to show what missing lists.
func unreadable(p Summary, missing []string, err error) string {
	return fmt.Sprintf("the downstream pipeline %d (in %s, started by the trigger job %q of pipeline %d) could not be read: %v. Its status (%s) is the one its trigger job reports. %s",
		p.ID, p.Project, p.Bridge, p.Parent, err, p.Status, lacking(p, missing))
}

// failedJobs adds the failed jobs of one pipeline to r. GitLab is asked
// for these only (scope[]=failed): a pipeline can have a thousand jobs. A
// job that failed and was retried is not among them: without
// include_retried, GitLab lists the latest run of each job.
func failedJobs(ctx context.Context, c *gitlab.Client, r *Report, n node, o Options) error {
	path := gitlab.Project(n.api) + "/pipelines/" + strconv.Itoa(n.ID) + "/jobs"
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
			Pipeline: n.ID, Project: n.shown, FailureReason: j.FailureReason, AllowFailure: j.AllowFailure,
			api: n.api,
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
			raw, err := c.GetText(ctx, gitlab.Project(jobs[i].api)+"/jobs/"+strconv.Itoa(jobs[i].ID)+"/trace", int64(lines)*400+32*1024)
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

// Denied reports whether err is GitLab refusing to show something to this
// token: HTTP 403, or 404, which is what it answers for a project (and all
// that is in it) the token cannot see at all.
func Denied(err error) bool {
	return gitlab.IsStatus(err, 403) || gitlab.IsStatus(err, 404)
}

// FromMR is the report of a pipeline of a merge request that could not be
// read (err) because it runs in a fork the token cannot read: what the
// merge request says of it (fork, see Found.Fork), and a warning for what
// is missing. The merge request is read again at each poll of "pipeline
// wait", so this status is as fresh as one read from the pipeline. It is
// all that such a wait goes by: the pipelines downstream of this one
// cannot be seen, and are not waited for.
func FromMR(fork Summary, err error) *Report {
	fork.Unreadable = true
	return &Report{
		Pipeline: fork,
		Running:  !mr.Finished(fork.Status),
		Warnings: []string{fmt.Sprintf("pipeline %d of this merge request runs in another project, %s (the fork the merge request comes from), which this token cannot read: %v. Its status (%s) is the one the merge request reports. %s",
			fork.ID, fork.Project, err, fork.Status, lacking(fork, []string{missingJobs, missingDownstream}))},
	}
}

// Found is what ForMR found for a commit of a merge request.
type Found struct {
	// ID is the pipeline of the commit, 0 when there is none: GitLab has
	// not created it yet, or never will.
	ID int
	// SHA is the commit: the head of the merge request (in full), or
	// the SHA asked for.
	SHA string
	// Head is the head commit of the merge request.
	Head string
	// Unconfirmed is the head pipeline of the merge request when ID is 0
	// and that pipeline may well be the pipeline of the head commit: it
	// runs on a merged-results or merge-train commit that cannot be read
	// (HTTP 404), so its parents could not be checked.
	Unconfirmed int
	// Unchecked is, when ID is 0, how many of the pipelines looked at
	// may or may not have run on SHA: their merge commit cannot be read
	// (HTTP 404).
	Unchecked int
	// Fork is the pipeline ID as the merge request reports it, when it
	// belongs to another project than the merge request: a merge request
	// from a fork has its pipelines in the fork (its source project),
	// except those a member of the parent project ran there. Nil when the
	// pipeline is in the project of the merge request.
	Fork *Summary
	// ForkProject names the project of Fork in API requests, by its
	// numeric ID: the pipeline is to be read from there. Fork.Project,
	// the path in the pipeline's web_url, is for display.
	ForkProject string
}

// ForMR finds the pipeline of a merge request for the commit sha (full or
// abbreviated; the head commit when empty): the head pipeline when it ran
// on that commit, else the newest of the last 20 pipelines of the merge
// request that did. A merged-results or merge-train pipeline runs on a
// temporary merge commit of the commit (see mr.PipelineOnCommit): that is
// looked into for the head pipeline, and for the others only when sha is
// given, since it costs a request for each of them.
//
// The pipeline is not always in the project of the merge request: see
// Found.Fork.
func ForMR(ctx context.Context, c *gitlab.Client, r ref.MR, sha string) (Found, error) {
	var m gitlab.MergeRequest
	if err := c.Get(ctx, mr.Base(r), nil, &m); err != nil {
		return Found{}, err
	}
	given := sha != ""
	head := !given || strings.HasPrefix(m.SHA, sha)
	if head {
		sha = m.SHA
	}
	found := Found{SHA: sha, Head: m.SHA}
	// take makes p the pipeline found. GitLab gives the project of each
	// pipeline (project_id), in head_pipeline as in the list.
	take := func(p gitlab.PipelineRef) {
		found.ID, found.Fork, found.ForkProject = p.ID, nil, ""
		if p.ProjectID != 0 && p.ProjectID != m.ProjectID {
			s := summary(p, strconv.Itoa(p.ProjectID))
			found.Fork, found.ForkProject = &s, strconv.Itoa(p.ProjectID)
		}
	}
	unconfirmed, unchecked := 0, 0
	hp := m.HeadPipeline
	if hp != nil {
		on, err := mr.PipelineOnCommit(ctx, c, m, *hp, sha)
		if err != nil {
			return found, err
		}
		if on == mr.OnCommitYes {
			take(*hp)
			return found, nil
		}
		if on == mr.OnCommitUnknown {
			unchecked++
			if head {
				unconfirmed = hp.ID
			}
		}
	}
	var list []gitlab.PipelineRef
	if err := c.Get(ctx, mr.Base(r)+"/pipelines", url.Values{"per_page": {"20"}}, &list); err != nil {
		return found, err
	}
	for _, p := range list {
		// GitLab lists the newest first: the pipelines older than one
		// that matches are not looked at, which spares their commits.
		if p.ID <= found.ID || (hp != nil && p.ID == hp.ID) {
			continue
		}
		if !given {
			// Waiting for the pipeline of a new head: the pipelines
			// listed are those of older commits, not worth a request
			// each.
			if strings.HasPrefix(p.SHA, sha) {
				take(p)
			}
			continue
		}
		on, err := mr.PipelineOnCommit(ctx, c, m, p, sha)
		if err != nil {
			return found, err
		}
		switch on {
		case mr.OnCommitYes:
			take(p)
		case mr.OnCommitUnknown:
			unchecked++
		}
	}
	if found.ID == 0 {
		found.Unconfirmed, found.Unchecked = unconfirmed, unchecked
	}
	return found, nil
}
