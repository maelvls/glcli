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

func openMR(ctx context.Context, rt *cli.Runtime, arg string, ignore, include []string) (*mrContext, error) {
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
	return &mrContext{ref: r, client: c, people: newPeople(me, ignore, include)}, nil
}

const includeAuthorUsage = "Usernames whose comments count although they look like bots (such as a service account); repeatable. --ignore-author wins for a username given to both"

// newPeople reads --ignore-author and --include-author: usernames, with
// or without "@".
func newPeople(me string, ignore, include []string) mr.People {
	p := mr.People{Me: me, Ignore: map[string]bool{}, Include: map[string]bool{}}
	for _, u := range ignore {
		p.Ignore[strings.TrimPrefix(u, "@")] = true
	}
	for _, u := range include {
		p.Include[strings.TrimPrefix(u, "@")] = true
	}
	return p
}

func newMRGet(rt *cli.Runtime) *cobra.Command {
	var (
		cursor      string
		description bool
		maxBody     int
		ignore      []string
		include     []string
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
			m, err := openMR(ctx, rt, args[0], ignore, include)
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
	cmd.Flags().StringSliceVar(&include, "include-author", nil, includeAuthorUsage)
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
		follow      bool
		maxBody     int
		ignore      []string
		include     []string
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

  push      new commits whose changes differ ("rebase_only": false), or
            that could not be compared ("rebase_only" absent)
  rebase    a push that keeps the same changes (rebase, reword:
            "rebase_only": true)
  comment   a new comment by someone else (bots left out)
  thread    a thread resolved or reopened
  approval  an approval given or withdrawn
  state     merged, closed or reopened
  pipeline  the head pipeline reaching a final status

A push is a rebase when GitLab's patch ID of the merge request (git
patch-id of its changes) is the same before and after. This needs
GitLab 18.2 or newer, whose patch IDs see whitespace, and a cursor that
carries one. One change still passes for a rebase: a push that rebases
and also moves a change to another place of its file, where the lines
around it are the same. Without patch IDs (older GitLab, a cursor
printed by an older glcli) glcli compares the two diffs itself, and they
must be identical: a rebase onto a target branch that added or removed
lines anywhere above a change in the same file, or changed the lines
next to it, then counts as a push.

--on defaults to push,comment,thread,approval,state. What I did myself
(my push, my approval, my resolve, my merge, my close) does not end the
wait unless --include-mine. Every event since the cursor is printed,
including those that did not end the wait, each with its "kind".

It polls every --interval (1m), keeps trying while GitLab cannot be
reached (closed lid, VPN down), and has no time limit unless --timeout.
Once the merge request is merged or closed, it returns whatever --on
says and whoever did it (nothing more will happen to it), with a hint
and no triggered_by when no event ended the wait. A cursor from another
merge request is refused.

Prints {events, mr, triggered_by}. mr.cursor is the cursor to wait again.
When --timeout runs out: events [], timed_out true, and mr.cursor is the
--cursor it started from. Exit codes: 0 (also on timeout), 1 on error,
130 when interrupted.

--follow does not exit at the first poll that ends the wait: it prints
one line of compact JSON for it and goes on, from that line's cursor,
until the merge request is merged or closed, --timeout, or an
interruption. Every line has a "type" and the "cursor" to resume from
(pass the last one read to --cursor):

  {"type":"events","events":[...],"triggered_by":[...],"mr":{...},"cursor":"..."}
  {"type":"warning","message":"cannot reach GitLab, trying again: ...","cursor":"..."}
  {"type":"end","reason":"merged|closed|timeout|interrupted","cursor":"..."}
  {"type":"error","error":"...","cursor":"..."}

An events line is what the wait would have printed (events that did not
end it are kept for the next one); merged or closed prints the last
events line, then the end line. A warning line comes once per outage,
and another when GitLab answers again. An error line comes before the
usual exit code and message on stderr.`,
		Example: `  glcli mr wait https://gitlab.com/group/project/-/merge_requests/123
  glcli mr wait group/project!123 --cursor eyJzIjoi... --timeout 2h
  glcli mr wait !123 --on state            # until merged or closed
  glcli mr wait !123 --on comment --ignore-author renovate`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			ctx := cmd.Context()
			var prev mr.Cursor
			var st *stream
			if follow {
				st = &stream{rt: rt, resume: func(l *streamLine) {
					if prev.SHA != "" {
						l.Cursor = prev.Encode()
					} else if c, err := mr.DecodeCursor(cursor); err == nil {
						l.Cursor = c.Encode()
					}
				}}
				defer func() { st.stop(ctx, err) }()
			}
			if err := w.check(); err != nil {
				return err
			}
			for _, t := range on {
				if !slices.Contains(mr.Triggers, t) {
					return usagef("--on %q: valid values are %s", t, strings.Join(mr.Triggers, ", "))
				}
			}
			m, err := openMR(ctx, rt, args[0], ignore, include)
			if err != nil {
				return err
			}
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
			if follow {
				return followMR(ctx, rt, st, m, d, &prev, mrFollowOptions{on: on, includeMine: includeMine, maxBody: maxBody, w: w})
			}
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
				if d.AwaitsPatchID(ctx, prev, s) {
					return false, nil // GitLab has not computed the patch ID of the push yet
				}
				last = s
				events, err := d.Events(ctx, prev, s)
				if err != nil {
					return false, err
				}
				triggered := triggeredBy(events, on, includeMine)
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
	cmd.Flags().BoolVar(&includeMine, "include-mine", false, "Let my own pushes, approvals, resolves, merges and closes end the wait too")
	cmd.Flags().BoolVar(&follow, "follow", false, "Do not exit: print one JSON line per poll that would have ended the wait, until merged or closed (see above)")
	cmd.Flags().IntVar(&maxBody, "max-body-chars", 4000, "Cut comment bodies after this many characters (0: no limit)")
	cmd.Flags().StringSliceVar(&ignore, "ignore-author", nil, "Usernames whose comments do not count (bots are already left out); repeatable")
	cmd.Flags().StringSliceVar(&include, "include-author", nil, includeAuthorUsage)
	addWaitFlags(cmd, &w, defaultMRInterval)
	return cmd
}

// triggeredBy lists the kinds of the events that end the wait, once
// each, in the order of the events: those in on, and mine only with
// includeMine.
func triggeredBy(events []mr.Event, on []string, includeMine bool) []string {
	var triggered []string
	for _, e := range events {
		if slices.Contains(on, e.Trigger()) && (!e.Mine || includeMine) && !slices.Contains(triggered, e.Trigger()) {
			triggered = append(triggered, e.Trigger())
		}
	}
	return triggered
}

type mrFollowOptions struct {
	on          []string
	includeMine bool
	maxBody     int
	w           waitFlags
}

// mrEventsLine is a line of "mr wait --follow" for a poll that would have
// ended the wait. The cursor is at the top, as in the other lines, and
// not in mr.
type mrEventsLine struct {
	Type        string     `json:"type"`
	Events      []mr.Event `json:"events"`
	TriggeredBy []string   `json:"triggered_by,omitempty"`
	MR          mr.View    `json:"mr"`
	Cursor      string     `json:"cursor"`
}

// followMR is the loop of "mr wait --follow": the loop of mr wait, which
// prints a line where mr wait would have exited, moves *prev to the
// cursor of that line, and goes on. *prev only moves when a line is
// printed: the events that did not end the wait (mine, kinds not in --on)
// are printed with the next line, as mr wait prints them with the events
// that end it, and every line's cursor is one from which a new mr wait
// misses nothing.
func followMR(ctx context.Context, rt *cli.Runtime, st *stream, m *mrContext, d *mr.Differ, prev *mr.Cursor, o mrFollowOptions) error {
	timedOut, err := rt.PollNotify(ctx, o.w.interval, o.w.timeout, func() (bool, error) {
		rt.ResetWarnings()
		s, err := mr.Fetch(ctx, m.client, m.ref)
		if err != nil {
			return false, err
		}
		st.answered()
		if err := prev.Check(s); err != nil {
			return false, usageError{err}
		}
		if s.Unsettled {
			return false, nil // a push is landing: look again next time
		}
		// At most two polls per diff version (see AwaitsPatchID): the
		// events that came with the push wait with it, and come out
		// together in the next line.
		if d.AwaitsPatchID(ctx, *prev, s) {
			return false, nil
		}
		events, err := d.Events(ctx, *prev, s)
		if err != nil {
			return false, err
		}
		triggered := triggeredBy(events, o.on, o.includeMine)
		final := s.MR.State == "merged" || s.MR.State == "closed"
		if len(triggered) == 0 && !final {
			return false, nil
		}
		next := mr.NewCursor(s)
		if len(events) > 0 {
			warnTruncated(rt, s)
			view := mr.NewView(s, m.people, mr.ViewOptions{MaxBodyChars: o.maxBody})
			view.Cursor = ""
			if err := rt.PrintLine(mrEventsLine{Type: "events", Events: events, TriggeredBy: triggered, MR: view, Cursor: next.Encode()}, "events"); err != nil {
				return false, err
			}
		}
		*prev = next
		if final {
			st.end(s.MR.State)
			return true, nil
		}
		return false, nil
	}, st.outage)
	if err != nil {
		return err
	}
	if timedOut {
		st.end("timeout")
	}
	return nil
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
			m, err := openMR(ctx, rt, args[0], nil, nil)
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
