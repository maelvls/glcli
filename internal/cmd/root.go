// Package cmd defines glcli's commands.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/gitlab"
)

const noTimeout = "glcli/no-timeout"

// defaultCommandTimeout cuts every command but the wait ones.
const defaultCommandTimeout = 30 * time.Second

// Run runs glcli with args and returns the exit code: 0, 1 on error, 2 on
// a usage error, 124 when the command timed out, 130 when interrupted.
func Run(args []string, stdout, stderr io.Writer) int {
	rt := cli.New(stdout, stderr)
	return run(rt, args)
}

func run(rt *cli.Runtime, args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := newRoot(rt)
	root.SetArgs(args)
	root.SetOut(rt.Stdout)
	root.SetErr(rt.Stderr)
	var cancel context.CancelFunc = func() {}
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Annotations[noTimeout] == "" {
			var c context.Context
			c, cancel = context.WithTimeout(cmd.Context(), commandTimeout())
			cmd.SetContext(c)
		}
		return nil
	}
	err := root.ExecuteContext(ctx)
	cancel()
	if err == nil {
		return 0
	}
	switch {
	case ctx.Err() != nil:
		fmt.Fprintln(rt.Stderr, "glcli: interrupted")
		return 130
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintf(rt.Stderr, "glcli: timed out after %s (GLCLI_COMMAND_TIMEOUT_MS); run again with GLCLI_DEBUG=1 to see the slow calls\n", commandTimeout())
		return 124
	}
	var usage usageError
	if errors.As(err, &usage) || isCobraUsage(err) {
		fmt.Fprintf(rt.Stderr, "glcli: %v\n", err)
		return 2
	}
	fmt.Fprintf(rt.Stderr, "glcli: %v\n", err)
	if gitlab.IsStatus(err, 401) {
		fmt.Fprintln(rt.Stderr, "glcli: the token was rejected: run `glab auth login --hostname <host>` (glcli reads glab's config), or `glcli auth status`")
	}
	return 1
}

// isCobraUsage recognises the usage errors cobra makes itself: wrong
// number of arguments, unknown command.
func isCobraUsage(err error) bool {
	msg := err.Error()
	return strings.HasPrefix(msg, "unknown command") ||
		strings.HasPrefix(msg, "accepts ") ||
		strings.HasPrefix(msg, "requires at least") ||
		strings.HasPrefix(msg, "requires at most") ||
		strings.Contains(msg, " arg(s), received ")
}

func commandTimeout() time.Duration {
	if v := os.Getenv("GLCLI_COMMAND_TIMEOUT_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return defaultCommandTimeout
}

type usageError struct{ error }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

func newRoot(rt *cli.Runtime) *cobra.Command {
	root := &cobra.Command{
		Use:   "glcli",
		Short: "GitLab CLI for AI agents",
		Long: `glcli is a GitLab CLI for AI agents. Every command prints one line of
compact JSON on stdout (indented on a terminal), with "warnings" and
"hints" inside it; errors are plain text on stderr.

It reads the hosts and tokens of glab (~/.config/glab-cli/config.yml), so
there is nothing to log in to. The wait commands block until what an agent
is waiting on happens, and print it:

  glcli inbox                what waits on me: review requests, replies, pushes
  glcli inbox wait           block until something new waits on me
  glcli mr get <mr>          the state of a merge request, with a cursor
  glcli mr wait <mr>         block until someone comments, pushes, resolves, approves or merges
  glcli mr discussions <mr>  the threads of a merge request
  glcli pipeline wait <mr>   block until the head pipeline (and its downstream ones) finish

A merge request is its URL, group/project!123, or !123 inside a git
checkout of the project.

Environment: GLCLI_HOST (default host), GLCLI_TOKEN or GITLAB_TOKEN (token
for the default host), GLCLI_DEBUG=1 (trace each API call on stderr),
GLCLI_COMMAND_TIMEOUT_MS (30000; the wait commands have no timeout).`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&rt.HostFlag, "host", "", "GitLab host, such as gitlab.com or code.pan.run (default: the URL's, the git remote's, or glab's default host)")
	root.PersistentFlags().StringVarP(&rt.RepoFlag, "repo", "R", "", "Project for short references such as !123: group/project or its URL")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.AddCommand(newMR(rt), newPipeline(rt), newInbox(rt), newAuth(rt))
	return root
}

// waitFlags are shared by the wait commands.
type waitFlags struct {
	interval time.Duration
	timeout  time.Duration
}

func addWaitFlags(cmd *cobra.Command, w *waitFlags, interval time.Duration) {
	cmd.Flags().DurationVar(&w.interval, "interval", interval, "How long to wait between two polls (at least 5s)")
	cmd.Flags().DurationVar(&w.timeout, "timeout", 0, "Give up after this long, such as 30m or 2h (0: wait until it happens or the command is interrupted)")
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[noTimeout] = "true"
}

func (w waitFlags) check() error {
	if w.interval < 5*time.Second {
		return usagef("--interval must be at least 5s, got %s", w.interval)
	}
	if w.timeout < 0 {
		return usagef("--timeout must be positive, got %s", w.timeout)
	}
	return nil
}
