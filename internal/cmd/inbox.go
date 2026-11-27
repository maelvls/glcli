package cmd

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/inbox"
	"github.com/maelvls/glcli/internal/mr"
)

type inboxFlags struct {
	since   string
	all     bool
	roles   []string
	maxBody int
	ignore  []string
}

var validRoles = []string{"review", "authored", "todo"}

// inbox without --since looks at the last two weeks: older to-do items are
// usually stale, and an agent should not wade through them every time.
const (
	defaultInboxWindow     = 14 * 24 * time.Hour
	defaultInboxWindowText = "14d"
)

func addInboxFlags(cmd *cobra.Command, f *inboxFlags) {
	cmd.Flags().StringVar(&f.since, "since", "", "Only items whose newest reason is after this: 30m, 12h, 2d, 2026-11-26, 2026-11-26T14:30 or RFC 3339")
	cmd.Flags().StringSliceVar(&f.roles, "role", validRoles, "Sources: review (MRs I review), authored (MRs I opened), todo (my to-do items)")
	cmd.Flags().IntVar(&f.maxBody, "max-body-chars", 500, "Cut to-do bodies after this many characters (0: no limit)")
	cmd.Flags().StringSliceVar(&f.ignore, "ignore-author", nil, "Usernames whose comments do not count (bots are already left out); repeatable")
}

type inboxResult struct {
	Me        string       `json:"me"`
	Host      string       `json:"host"`
	Items     []inbox.Item `json:"items"`
	CheckedAt time.Time    `json:"checked_at"`
	TimedOut  bool         `json:"timed_out,omitempty"`
}

func collectInbox(ctx context.Context, rt *cli.Runtime, f inboxFlags) (*inboxResult, []inbox.Item, error) {
	for _, r := range f.roles {
		if !slices.Contains(validRoles, r) {
			return nil, nil, usagef("--role %q: valid values are %s", r, strings.Join(validRoles, ", "))
		}
	}
	host, err := rt.DefaultHost()
	if err != nil {
		return nil, nil, err
	}
	c, err := rt.Client(host)
	if err != nil {
		return nil, nil, err
	}
	me, err := rt.Me(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	people := mr.People{Me: me, Ignore: map[string]bool{}}
	for _, u := range f.ignore {
		people.Ignore[strings.TrimPrefix(u, "@")] = true
	}
	checkedAt := rt.Now().Round(0).Truncate(time.Second)
	res, err := inbox.Collect(ctx, c, inbox.Options{
		Host:   host,
		People: people,
		Roles: inbox.Roles{
			Review:   slices.Contains(f.roles, "review"),
			Authored: slices.Contains(f.roles, "authored"),
			Todo:     slices.Contains(f.roles, "todo"),
		},
		MaxBodyChars: f.maxBody,
	})
	if err != nil {
		return nil, nil, err
	}
	for _, w := range res.Warnings {
		rt.Warnf("%s", w)
	}
	return &inboxResult{Me: me, Host: host, CheckedAt: checkedAt}, res.Items, nil
}

func filterItems(items []inbox.Item, since time.Time, all bool) []inbox.Item {
	var out []inbox.Item
	for _, it := range items {
		if !all && !it.Waiting() {
			continue
		}
		if !since.IsZero() && !it.At.After(since) {
			continue
		}
		out = append(out, it)
	}
	return out
}

func newInbox(rt *cli.Runtime) *cobra.Command {
	var f inboxFlags
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "List what waits on me across all projects",
		Long: `List what waits on me across all the projects of a GitLab host (--host,
or the default one), newest first:

  review    open merge requests where I am a reviewer, with the reasons:
            review_requested (never reviewed, or review requested again
            after my last action), new_commits (pushed after my last
            comment, and not approved by me; a rebase that keeps GitLab's
            patch ID of the changes does not count), new_replies (in a
            thread I took part in, or mentioning me, after my last comment)
  authored  my open merge requests: new_comments (after my last comment
            or push), pipeline_failed, ready_to_merge
  todo      my pending to-do items (mentions, directly addressed, approval
            required, failed builds...); those about a merge request above
            are merged into it as todo:<action> reasons

Only items with a reason are listed unless --all. "at" is when the newest
reason happened; --since keeps the items whose "at" is after it (default:
the last 14 days, with a hint counting what is older).
"checked_at" is when the check started: pass it to "inbox wait --since"
to wait for what comes next without missing anything.

Prints {me, host, items: [{kind, reasons, ref, title, url, author, at,
draft, approved_by_me, unresolved_threads, pipeline, merge_status,
todo_ids, body}], checked_at}.`,
		Example: `  glcli inbox
  glcli inbox --since 2d --role review
  glcli inbox --host code.pan.run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			since, err := cli.ParseSince(f.since, rt.Now())
			if err != nil {
				return usageError{err}
			}
			window := !cmd.Flags().Changed("since")
			if window {
				since = rt.Now().Add(-defaultInboxWindow)
			}
			res, items, err := collectInbox(ctx, rt, f)
			if err != nil {
				return err
			}
			res.Items = filterItems(items, since, f.all)
			if hidden := len(filterItems(items, time.Time{}, f.all)) - len(res.Items); window && hidden > 0 {
				rt.Hintf("%d older items (before the last %s) are left out: pass --since 90d to see them.", hidden, defaultInboxWindowText)
			}
			return rt.Print(res, "items")
		},
	}
	addInboxFlags(cmd, &f)
	cmd.Flags().BoolVar(&f.all, "all", false, "Also list the merge requests that wait on nobody but their author or other reviewers")
	cmd.AddCommand(newInboxWait(rt))
	return cmd
}

func newInboxWait(rt *cli.Runtime) *cobra.Command {
	var (
		f inboxFlags
		w waitFlags
	)
	cmd := &cobra.Command{
		Use:   "wait",
		Short: "Wait until something new waits on me, then print it",
		Long: `Wait until an inbox item has a reason newer than --since (by default,
when the command starts), then print those items and exit: the way to
learn that someone asked for my review, replied to me, or pushed on a
merge request I review.

Pass the "checked_at" of the previous inbox or inbox wait as --since so
that nothing is missed between two commands.

It polls every --interval (2m), keeps trying while GitLab cannot be
reached, and has no time limit unless --timeout. Prints what "inbox"
prints; on timeout, items is [], timed_out is true, and checked_at is the
--since to wait again with. Exit codes: 0 (also on timeout), 1 on error,
130 when interrupted.`,
		Example: `  glcli inbox wait
  glcli inbox wait --since 2026-11-26T14:30:00+01:00 --timeout 4h
  glcli inbox wait --role review,todo --interval 5m`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := w.check(); err != nil {
				return err
			}
			since, err := cli.ParseSince(f.since, rt.Now())
			if err != nil {
				return usageError{err}
			}
			if since.IsZero() {
				since = rt.Now().Round(0).Truncate(time.Second)
			}
			var res *inboxResult
			timedOut, err := rt.Poll(ctx, w.interval, w.timeout, func() (bool, error) {
				rt.ResetWarnings()
				r, items, err := collectInbox(ctx, rt, f)
				if err != nil {
					return false, err
				}
				res = r
				res.Items = filterItems(items, since, false)
				return len(res.Items) > 0, nil
			})
			if err != nil {
				return err
			}
			if timedOut {
				if res == nil {
					res = &inboxResult{}
				}
				res.Items, res.TimedOut, res.CheckedAt = nil, true, since
				rt.Hintf("Nothing new within %s: to wait again, pass --since %s.", w.timeout, since.Format(time.RFC3339))
			}
			return rt.Print(res, "items")
		},
	}
	addInboxFlags(cmd, &f)
	addWaitFlags(cmd, &w, defaultInboxInterval)
	return cmd
}
