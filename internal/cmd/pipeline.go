package cmd

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/gitlab"
	"github.com/maelvls/glcli/internal/pipeline"
	"github.com/maelvls/glcli/internal/ref"
)

const (
	defaultMRInterval       = time.Minute
	defaultPipelineInterval = 30 * time.Second
	defaultInboxInterval    = 2 * time.Minute
)

// defaultCreateTimeout is how long "pipeline wait" waits for GitLab to
// create the pipeline of a commit before saying that there is none.
// GitLab creates it in a background job: seconds after the push as a
// rule, a few minutes when its queues are behind. Longer than that, and a
// commit that will never have a pipeline keeps an agent waiting for
// nothing.
const defaultCreateTimeout = 5 * time.Minute

func newPipeline(rt *cli.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipeline",
		Short: "Read and wait on pipelines",
	}
	cmd.AddCommand(newPipelineGet(rt), newPipelineWait(rt))
	return cmd
}

type pipelineFlags struct {
	sha            string
	logLines       int
	includeAllowed bool
	noDownstream   bool
}

func addPipelineFlags(cmd *cobra.Command, f *pipelineFlags) {
	cmd.Flags().StringVar(&f.sha, "sha", "", "For a merge request: the pipeline of this commit (default: the head commit)")
	cmd.Flags().IntVar(&f.logLines, "log-lines", 40, "Lines of log to print for each failed job (0: none)")
	cmd.Flags().BoolVar(&f.includeAllowed, "include-allowed", false, "Also list failed jobs that are allowed to fail")
	cmd.Flags().BoolVar(&f.noDownstream, "no-downstream", false, "Do not follow trigger jobs into downstream pipelines")
}

func (f pipelineFlags) options(logs bool) pipeline.Options {
	o := pipeline.Options{IncludeAllowed: f.includeAllowed, Downstream: !f.noDownstream, MaxDepth: 4}
	if logs {
		o.LogLines = f.logLines
	}
	return o
}

// pipelineTarget resolves the argument to a project and a pipeline ID
// (0 while the pipeline of a merge request's commit does not exist yet).
type pipelineTarget struct {
	client  *gitlab.Client
	project string
	id      int
	mr      *ref.MR
	sha     string
}

func openPipeline(rt *cli.Runtime, arg string) (*pipelineTarget, error) {
	p, ok, err := ref.ParsePipeline(arg)
	if err != nil {
		return nil, usageError{err}
	}
	if ok {
		c, err := rt.Client(p.Host)
		if err != nil {
			return nil, err
		}
		return &pipelineTarget{client: c, project: p.Project, id: p.ID}, nil
	}
	r, err := rt.MR(arg)
	if err != nil {
		return nil, usageError{err}
	}
	c, err := rt.Client(r.Host)
	if err != nil {
		return nil, err
	}
	return &pipelineTarget{client: c, project: r.Project, mr: &r}, nil
}

// resolve finds the pipeline of the merge request's commit.
func (t *pipelineTarget) resolve(ctx context.Context, sha string) error {
	if t.mr == nil {
		return nil
	}
	id, head, err := pipeline.ForMR(ctx, t.client, *t.mr, sha)
	if err != nil {
		return err
	}
	t.id, t.sha = id, head
	return nil
}

type pipelineResult struct {
	*pipeline.Report
	// SHA is the commit whose pipeline was looked for, when there is none yet.
	SHA      string `json:"sha,omitempty"`
	TimedOut bool   `json:"timed_out,omitempty"`
	// NoPipeline is true when "pipeline wait" stopped because GitLab
	// created no pipeline for SHA within --create-timeout. Never set
	// together with TimedOut.
	NoPipeline bool `json:"no_pipeline,omitempty"`
}

// noPipelineCauses is why a commit of a merge request has no pipeline,
// for the hints of "pipeline get" and "pipeline wait".
const noPipelineCauses = "the project has no CI configuration, the push skipped CI ([skip ci] in the commit message, or the ci.skip push option), or the rules of .gitlab-ci.yml (rules, workflow:rules) create no pipeline for this change"

func newPipelineGet(rt *cli.Runtime) *cobra.Command {
	var f pipelineFlags
	cmd := &cobra.Command{
		Use:   "get <mr|pipeline-url>",
		Short: "Print a pipeline, its downstream pipelines and its failed jobs",
		Long: `Print a pipeline: its status, its downstream pipelines (started by
trigger jobs, followed up to 4 levels), and the failed jobs of all of them
with the end of their log.

The argument is a pipeline URL, or a merge request (its URL,
group/project!123 or !123): then the pipeline of its head commit, or of
--sha.

Prints {pipeline, downstream, failed_jobs, running, allowed_failures}.
"running" is true while any pipeline of the tree has not finished.`,
		Example: `  glcli pipeline get !123
  glcli pipeline get https://gitlab.com/group/project/-/pipelines/456 --log-lines 100`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			t, err := openPipeline(rt, args[0])
			if err != nil {
				return err
			}
			if err := t.resolve(ctx, f.sha); err != nil {
				return err
			}
			if t.id == 0 {
				rt.Hintf("No pipeline for %s: GitLab may still be creating it (it takes a few seconds after a push), or this commit will never have one (%s). `glcli pipeline wait` waits for it, and prints \"no_pipeline\": true when none is created.", t.sha, noPipelineCauses)
				return rt.Print(pipelineResult{Report: &pipeline.Report{Running: true}, SHA: t.sha}, "failed_jobs")
			}
			r, err := pipeline.Collect(ctx, t.client, t.project, t.id, f.options(true))
			if err != nil {
				return err
			}
			return rt.Print(pipelineResult{Report: r}, "failed_jobs")
		},
	}
	addPipelineFlags(cmd, &f)
	return cmd
}

func newPipelineWait(rt *cli.Runtime) *cobra.Command {
	var (
		f             pipelineFlags
		w             waitFlags
		createTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "wait <mr|pipeline-url>",
		Short: "Wait until a pipeline and its downstream pipelines finish, then print them",
		Long: `Wait until a pipeline and its downstream pipelines have all finished
(success, failed, canceled, skipped, or manual: waiting for someone to
start a job), then print what "pipeline get" prints.

For a merge request, it waits for the pipeline of --sha, or of the head
commit, following the head when someone pushes during the wait; right
after a push, while GitLab has not created the pipeline yet, it waits for
it to appear.

It does not wait for a pipeline that never comes: if GitLab has created
none within --create-timeout (5m, counted again from each new head
commit), it stops and prints {sha, no_pipeline: true, running: false,
failed_jobs: []} without "pipeline". The commit then most likely has no
pipeline at all: the project has no CI configuration, the push skipped CI
([skip ci], or the ci.skip push option), or the rules of .gitlab-ci.yml
create no pipeline for this change. --create-timeout 0 waits for it
indefinitely.

It polls every --interval (30s), keeps trying while GitLab cannot be
reached, and has no time limit unless --timeout; on timeout it prints the
current state with "timed_out": true (and never "no_pipeline": whichever
of --timeout and --create-timeout comes first decides). Exit codes: 0
(whatever the pipeline's status, on timeout, and when there is no
pipeline), 1 on error, 130 when interrupted.`,
		Example: `  glcli pipeline wait !123
  glcli pipeline wait group/project!123 --sha 1a2b3c4 --timeout 1h
  glcli pipeline wait https://gitlab.com/group/project/-/pipelines/456`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := w.check(); err != nil {
				return err
			}
			if createTimeout < 0 {
				return usagef("--create-timeout must be positive, got %s", createTimeout)
			}
			t, err := openPipeline(rt, args[0])
			if err != nil {
				return err
			}
			var (
				last   *pipeline.Report
				lastID int
				// GitLab has had since createSince to create the pipeline
				// of the commit createSHA.
				createSHA   string
				createSince time.Time
				noPipeline  bool
			)
			timedOut, err := rt.Poll(ctx, w.interval, w.timeout, func() (bool, error) {
				if err := t.resolve(ctx, f.sha); err != nil {
					return false, err
				}
				if t.id == 0 {
					// The report of an earlier head says nothing of this commit.
					last = nil
					// The wall clock, as in Poll: GitLab kept working while
					// this machine slept, and it has just answered that
					// there is still no pipeline.
					now := rt.Now().Round(0)
					if createSince.IsZero() || createSHA != t.sha {
						createSHA, createSince = t.sha, now
						if createTimeout > 0 {
							rt.Notef("no pipeline for %s yet: waiting up to %s for GitLab to create it (--create-timeout)", t.sha, createTimeout)
						} else {
							rt.Notef("no pipeline for %s yet: waiting for GitLab to create it", t.sha)
						}
					}
					noPipeline = createTimeout > 0 && now.Sub(createSince) >= createTimeout
					return noPipeline, nil
				}
				createSince = time.Time{}
				if t.id != lastID && lastID != 0 {
					rt.Notef("now waiting for pipeline %d (new head %s)", t.id, t.sha)
				}
				lastID = t.id
				r, err := pipeline.Collect(ctx, t.client, t.project, t.id, f.options(false))
				if err != nil {
					return false, err
				}
				last = r
				return !r.Running, nil
			})
			if err != nil {
				return err
			}
			if noPipeline {
				rt.Hintf("GitLab created no pipeline for %s within %s (--create-timeout), so there is nothing to wait for. The likely causes: %s. If a pipeline is still expected, run the same command again, with a longer --create-timeout.", t.sha, createTimeout, noPipelineCauses)
				return rt.Print(pipelineResult{Report: &pipeline.Report{}, SHA: t.sha, NoPipeline: true}, "failed_jobs")
			}
			if last == nil {
				rt.Hintf("No pipeline was created for %s within %s (--timeout): run the same command again to keep waiting for it.", t.sha, w.timeout)
				return rt.Print(pipelineResult{Report: &pipeline.Report{Running: true}, SHA: t.sha, TimedOut: true}, "failed_jobs")
			}
			if !timedOut && f.logLines > 0 && len(last.FailedJobs) > 0 {
				// Fetch once more with logs, now that it has finished.
				if r, err := pipeline.Collect(ctx, t.client, t.project, t.id, f.options(true)); err == nil {
					last = r
				}
			}
			if timedOut {
				rt.Hintf("The pipeline was still running after %s: run the same command again to keep waiting.", w.timeout)
			}
			return rt.Print(pipelineResult{Report: last, TimedOut: timedOut}, "failed_jobs")
		},
	}
	addPipelineFlags(cmd, &f)
	addWaitFlags(cmd, &w, defaultPipelineInterval)
	cmd.Flags().DurationVar(&createTimeout, "create-timeout", defaultCreateTimeout, "For a merge request: how long to wait for GitLab to create the pipeline before printing \"no_pipeline\": true (0: wait until it exists)")
	return cmd
}
