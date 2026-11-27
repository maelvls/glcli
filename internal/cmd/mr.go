package cmd

import (
	"context"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/gitlab"
	"github.com/maelvls/glcli/internal/mr"
	"github.com/maelvls/glcli/internal/ref"
)

func newMR(rt *cli.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mr",
		Short: "Read and wait on merge requests",
	}
	cmd.AddCommand(newMRGet(rt), newMRWait(rt), newMRDiscussions(rt))
	return cmd
}

// mrContext is what every mr command needs.
type mrContext struct {
	ref    ref.MR
	client *gitlab.Client
	people mr.People
}

func openMR(ctx context.Context, rt *cli.Runtime, arg string, ignore []string) (*mrContext, error) {
	r, err := rt.MR(arg)
	if err != nil {
		return nil, usageError{err}
	}
	c, err := rt.Client(r.Host)
	if err != nil {
		return nil, err
	}
	me, err := rt.Me(ctx, c)
	if err != nil {
		return nil, err
	}
	p := mr.People{Me: me, Ignore: map[string]bool{}}
	for _, u := range ignore {
		p.Ignore[strings.TrimPrefix(u, "@")] = true
	}
	return &mrContext{ref: r, client: c, people: p}, nil
}

func newMRGet(rt *cli.Runtime) *cobra.Command {
	var (
		cursor      string
		description bool
		maxBody     int
		ignore      []string
	)
	cmd := &cobra.Command{
		Use:   "get <mr>",
		Short: "Print the state of a merge request, and what changed since a cursor",
		Long: `Print the compact state of a merge request: head commit and when it was
pushed, merge status, approvals, head pipeline, thread counts, my last
comment and the last comment by someone else, and a "cursor".

With --cursor (the "cursor" of an earlier mr get or mr wait), "events"
lists what happened since, the same events as mr wait, without waiting.

Prints {ref, url, title, state, ..., cursor, events}.`,
		Example: `  glcli mr get https://gitlab.com/group/project/-/merge_requests/123
  glcli mr get group/project!123 --description
  glcli mr get !123 --cursor eyJzIjoi...`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			var prev mr.Cursor
			if cursor != "" {
				var err error
				if prev, err = mr.DecodeCursor(cursor); err != nil {
					return usageError{err}
				}
			}
			m, err := openMR(ctx, rt, args[0], ignore)
			if err != nil {
				return err
			}
			s, err := mr.Fetch(ctx, m.client, m.ref)
			if err != nil {
				return err
			}
			warnTruncated(rt, s)
			out := struct {
				mr.View
				Events []mr.Event `json:"events,omitempty"`
			}{View: mr.NewView(s, m.people, mr.ViewOptions{Description: description, MaxBodyChars: maxBody})}
			if cursor == "" {
				return rt.Print(out)
			}
			if err := prev.Check(s); err != nil {
				return usageError{err}
			}
			d := &mr.Differ{Client: m.client, People: m.people, MaxBodyChars: maxBody}
			if out.Events, err = d.Events(ctx, prev, s); err != nil {
				return err
			}
			return rt.Print(out, "events")
		},
	}
	cmd.Flags().StringVar(&cursor, "cursor", "", "Also list the events since this cursor (from an earlier mr get or mr wait)")
	cmd.Flags().BoolVar(&description, "description", false, "Include the description")
	cmd.Flags().IntVar(&maxBody, "max-body-chars", 4000, "Cut the description and comment bodies after this many characters (0: no limit)")
	cmd.Flags().StringSliceVar(&ignore, "ignore-author", nil, "Usernames whose comments do not count as human (bots are already left out); repeatable")
	return cmd
}

func warnTruncated(rt *cli.Runtime, s *mr.Snapshot) {
	if s.DiscussionsTruncated {
		rt.Warnf("more than 2000 discussions: only the first 2000 were read")
	}
}

func newMRWait(rt *cli.Runtime) *cobra.Command {
	var (
		cursor      string
		on          []string
		includeMine bool
		maxBody     int
		ignore      []string
		w           waitFlags
	)
	cmd := &cobra.Command{
		Use:   "wait <mr>",
		Short: "Wait until something happens on a merge request, then print it",
		Long: `Wait until something happens on a merge request, then print the events
and the new state, and exit. The way to wait for a reply, a push after a
review, an approval or a merge.

Events are computed against --cursor (from an earlier mr get or mr wait),
so nothing that happened in between is missed; without it, against the
state when the command starts. Kinds of events, selected with --on:

  push      new commits whose changes differ ("rebase_only": false)
  rebase    a push that keeps the same changes (rebase, reword)
  comment   a new comment by someone else (bots left out)
  thread    a thread resolved or reopened
  approval  an approval given or withdrawn
  state     merged, closed or reopened
  pipeline  the head pipeline reaching a final status

--on defaults to push,comment,thread,approval,state. What I did myself
(my push, my approval, my resolve) does not end the wait unless
--include-mine. Every event since the cursor is printed, including those
that did not end the wait, each with its "kind".

It polls every --interval (1m), keeps trying while GitLab cannot be
reached (closed lid, VPN down), and has no time limit unless --timeout.
Once the merge request is merged or closed, it returns whatever --on
says (nothing more will happen to it), with a hint when nothing in --on
happened. A cursor from another merge request is refused.

Prints {events, mr, triggered_by}. mr.cursor is the cursor to wait again.
When --timeout runs out: events [], timed_out true, and mr.cursor is the
--cursor it started from. Exit codes: 0 (also on timeout), 1 on error,
130 when interrupted.`,
		Example: `  glcli mr wait https://gitlab.com/group/project/-/merge_requests/123
  glcli mr wait group/project!123 --cursor eyJzIjoi... --timeout 2h
  glcli mr wait !123 --on state            # until merged or closed
  glcli mr wait !123 --on comment --ignore-author renovate`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := w.check(); err != nil {
				return err
			}
			for _, t := range on {
				if !slices.Contains(mr.Triggers, t) {
					return usagef("--on %q: valid values are %s", t, strings.Join(mr.Triggers, ", "))
				}
			}
			m, err := openMR(ctx, rt, args[0], ignore)
			if err != nil {
				return err
			}
			var prev mr.Cursor
			if cursor != "" {
				if prev, err = mr.DecodeCursor(cursor); err != nil {
					return usageError{err}
				}
			} else {
				s, err := mr.Fetch(ctx, m.client, m.ref)
				if err != nil {
					return err
				}
				prev = mr.NewCursor(s)
			}
			d := &mr.Differ{Client: m.client, People: m.people, MaxBodyChars: maxBody}
			type result struct {
				Events      []mr.Event `json:"events"`
				TriggeredBy []string   `json:"triggered_by,omitempty"`
				MR          mr.View    `json:"mr"`
				TimedOut    bool       `json:"timed_out,omitempty"`
			}
			var res result
			var last *mr.Snapshot
			timedOut, err := rt.Poll(ctx, w.interval, w.timeout, func() (bool, error) {
				s, err := mr.Fetch(ctx, m.client, m.ref)
				if err != nil {
					return false, err
				}
				if err := prev.Check(s); err != nil {
					return false, usageError{err}
				}
				if s.Unsettled {
					return false, nil // a push is landing: look again next time
				}
				last = s
				events, err := d.Events(ctx, prev, s)
				if err != nil {
					return false, err
				}
				var triggered []string
				for _, e := range events {
					if slices.Contains(on, e.Trigger()) && (!e.Mine || includeMine) && !slices.Contains(triggered, e.Trigger()) {
						triggered = append(triggered, e.Trigger())
					}
				}
				// Merged or closed, whoever did it and whatever --on says:
				// nothing more will happen, so do not wait forever.
				final := s.MR.State == "merged" || s.MR.State == "closed"
				if len(triggered) == 0 && !final {
					return false, nil
				}
				if final && len(triggered) == 0 {
					rt.Hintf("The merge request is %s: nothing more will happen to it.", s.MR.State)
				}
				res = result{Events: events, TriggeredBy: triggered}
				return true, nil
			})
			if err != nil {
				return err
			}
			if last != nil {
				warnTruncated(rt, last)
				res.MR = mr.NewView(last, m.people, mr.ViewOptions{MaxBodyChars: maxBody})
			}
			if timedOut {
				res = result{MR: res.MR, TimedOut: true}
				res.MR.Cursor = prev.Encode()
				rt.Hintf("Nothing to wait for happened within %s: to wait again without missing anything, pass --cursor %s.", w.timeout, res.MR.Cursor)
			}
			return rt.Print(res, "events")
		},
	}
	cmd.Flags().StringVar(&cursor, "cursor", "", "Compute events against this cursor (from mr get or mr wait) instead of the state at start")
	cmd.Flags().StringSliceVar(&on, "on", mr.DefaultTriggers, "Kinds of event that end the wait: "+strings.Join(mr.Triggers, ", "))
	cmd.Flags().BoolVar(&includeMine, "include-mine", false, "Let my own pushes, approvals, resolves and merges end the wait too")
	cmd.Flags().IntVar(&maxBody, "max-body-chars", 4000, "Cut comment bodies after this many characters (0: no limit)")
	cmd.Flags().StringSliceVar(&ignore, "ignore-author", nil, "Usernames whose comments do not count (bots are already left out); repeatable")
	addWaitFlags(cmd, &w, defaultMRInterval)
	return cmd
}

func newMRDiscussions(rt *cli.Runtime) *cobra.Command {
	var (
		unresolved    bool
		mine          bool
		includeSystem bool
		cursor        string
		maxBody       int
	)
	cmd := &cobra.Command{
		Use:     "discussions <mr>",
		Aliases: []string{"threads"},
		Short:   "List the threads of a merge request",
		Long: `List the threads (discussions) of a merge request, oldest first, with
their notes. System notes ("added 1 commit") are left out unless
--include-system.

--cursor keeps the threads with a note newer than the cursor, and in them
the first note (for context) and the newer ones.

Prints {ref, url, discussions: [{id, resolvable, resolved, file, line,
started_by, url, notes: [{id, author, at, body}]}]}. The id of a thread is
what a reply or a resolve needs.`,
		Example: `  glcli mr discussions !123 --unresolved
  glcli mr discussions group/project!123 --mine
  glcli mr discussions !123 --cursor eyJzIjoi...`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			f := mr.ThreadFilter{Unresolved: unresolved, Mine: mine, IncludeSystem: includeSystem, MaxBodyChars: maxBody}
			var prev mr.Cursor
			if cursor != "" {
				var err error
				if prev, err = mr.DecodeCursor(cursor); err != nil {
					return usageError{err}
				}
				f.AfterNote = prev.MaxNote
			}
			m, err := openMR(ctx, rt, args[0], nil)
			if err != nil {
				return err
			}
			s, err := mr.Fetch(ctx, m.client, m.ref)
			if err != nil {
				return err
			}
			if err := prev.Check(s); err != nil {
				return usageError{err}
			}
			warnTruncated(rt, s)
			return rt.Print(struct {
				Ref         string          `json:"ref"`
				URL         string          `json:"url"`
				Discussions []mr.ThreadView `json:"discussions"`
			}{s.Ref.String(), s.MR.WebURL, mr.Threads(s, m.people, f)}, "discussions")
		},
	}
	cmd.Flags().BoolVar(&unresolved, "unresolved", false, "Only unresolved threads")
	cmd.Flags().BoolVar(&mine, "mine", false, "Only threads I started or replied to")
	cmd.Flags().BoolVar(&includeSystem, "include-system", false, "Include system notes")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Only threads with notes newer than this cursor")
	cmd.Flags().IntVar(&maxBody, "max-body-chars", 4000, "Cut note bodies after this many characters (0: no limit)")
	return cmd
}
