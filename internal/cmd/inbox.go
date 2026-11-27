package cmd

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/inbox"
)

type inboxFlags struct {
	since   string
	all     bool
	roles   []string
	maxBody int
	ignore  []string
	include []string
	// cache is set by inbox wait, which collects again and again.
	cache *inbox.Cache
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
	cmd.Flags().StringSliceVar(&f.include, "include-author", nil, includeAuthorUsage)
}

type inboxResult struct {
	Me        string       `json:"me"`
	Host      string       `json:"host"`
	Items     []inbox.Item `json:"items"`
	CheckedAt time.Time    `json:"checked_at"`
	TimedOut  bool         `json:"timed_out,omitempty"`
	// unread are the merge requests that could not be read: see
	// inbox.Result.
	unread []string
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
	people := newPeople(me, f.ignore, f.include)
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
		Cache:        f.cache,
	})
	if err != nil {
		return nil, nil, err
	}
	for _, w := range res.Warnings {
		rt.Warnf("%s", w)
	}
	return &inboxResult{Me: me, Host: host, CheckedAt: checkedAt, unread: res.Unread}, res.Items, nil
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
            after my last review: a comment, an approval or its removal,
            requested changes; a push of mine is not a review),
            new_commits (pushed by someone else after my last review,
            and not approved by me; on GitLab 18.2 or newer, a rebase
            that keeps the patch ID of the changes does not count, and
            "at" is when the change I have not seen arrived; on older
            ones every push counts), new_replies (in a thread I took part
            in, or mentioning me, after my last comment or push)
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
		f      inboxFlags
		w      waitFlags
		follow bool
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
reached, and has no time limit unless --timeout. Between two polls, a
merge request whose updated_at did not change is not read again (my own
ones: only their pipeline and merge status; those I approved: only their
approvals), except once every 20 minutes, and right after a change. So
what GitLab does without changing updated_at, such as a push that leaves
no "added N commits" note, can be reported late: up to 20 minutes after
that merge request was last read in full, rounded up to a poll (longer
if --interval is longer). When something is new, everything is read
again before it is printed.

Prints what "inbox" prints; on timeout, items is [], timed_out is true,
and checked_at is the --since to wait again with. Exit codes: 0 (also on
timeout), 1 on error, 130 when interrupted.

--follow does not exit when something is new: it prints one line of
compact JSON with the new items and goes on, until --timeout or an
interruption. Every line has a "type" and the "checked_at" to resume from
(pass the last one read to --since):

  {"type":"items","items":[...],"checked_at":"..."}
  {"type":"warning","message":"cannot reach GitLab, trying again: ...","checked_at":"..."}
  {"type":"end","reason":"timeout|interrupted","checked_at":"..."}
  {"type":"error","error":"...","checked_at":"..."}

An item is printed again only when it gains a reason or its "at" moves
forward (not when a reason goes away). One that shows up late with an older "at" (a review requested on an old
merge request) is printed too, but only by a stream that was already
running: a new one only sees what is newer than its --since. A warning
line comes once per outage, and another when GitLab answers again. An
error line comes before the usual exit code and message on stderr.`,
		Example: `  glcli inbox wait
  glcli inbox wait --since 2026-11-26T14:30:00+01:00 --timeout 4h
  glcli inbox wait --role review,todo --interval 5m
  glcli inbox wait --since 2026-11-26T14:30:00+01:00 --follow`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			ctx := cmd.Context()
			var resume time.Time
			var st *stream
			if follow {
				st = &stream{rt: rt, resume: func(l *streamLine) {
					if !resume.IsZero() {
						l.CheckedAt = &resume
					}
				}}
				defer func() { st.stop(ctx, err) }()
			}
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
			f.cache = inbox.NewCache(rt.Now)
			if follow {
				resume = since
				return followInbox(ctx, rt, st, f, w, since, &resume)
			}
			var res *inboxResult
			check := func() (bool, error) {
				rt.ResetWarnings()
				r, items, err := collectInbox(ctx, rt, f)
				if err != nil {
					return false, err
				}
				res = r
				res.Items = filterItems(items, since, false)
				return len(res.Items) > 0, nil
			}
			timedOut, err := rt.Poll(ctx, w.interval, w.timeout, func() (bool, error) {
				if done, err := check(); !done || err != nil || !f.cache.Kept() {
					return done, err
				}
				// The checked_at printed with what is new is the --since of
				// the next wait: what happened before it on a merge request
				// that was not read again (see inbox.Cache) would be lost
				// for good. So everything is read before anything is
				// printed, and the wait goes on if nothing is new after all.
				f.cache.Expire()
				return check()
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
	cmd.Flags().BoolVar(&follow, "follow", false, "Do not exit: print one JSON line each time something is new (see above)")
	return cmd
}

// inboxItemsLine is a line of "inbox wait --follow".
type inboxItemsLine struct {
	Type      string       `json:"type"`
	Items     []inbox.Item `json:"items"`
	CheckedAt time.Time    `json:"checked_at"`
}

// followInbox is the loop of "inbox wait --follow". inbox has no cursor,
// only times. The stream remembers the last state of each waiting item
// (its reasons and "at") and prints an item when it is new: never seen,
// a reason that the last state did not have, or an "at" later than the
// last state's. A reason that goes away is not news (the item is not
// printed again), but the state is still recorded, so the reason counts
// as new if it comes back. An item that stops waiting keeps its last
// state: back unchanged, it is not printed again.
//
// The first time an item is read, an "at" older than since sets it aside
// as already known, as inbox wait does: at the first poll, and for a
// merge request that could not be read then (inbox.Result.Unread), at
// its first successful read. After that an item is new whatever its
// "at", so one that shows up late with an older "at" (a review requested
// on an old merge request, a to-do item dated in the past) is printed
// once, where waiting again with --since would not see it.
//
// What is printed comes from a poll that read everything, as in inbox
// wait (see inbox.Cache): its checked_at is a --since from which a new
// wait misses nothing that this line did not print, and becomes the
// checked_at of the lines that do not carry results (*resume).
func followInbox(ctx context.Context, rt *cli.Runtime, st *stream, f inboxFlags, w waitFlags, since time.Time, resume *time.Time) error {
	last := map[string]inbox.Item{}
	first := true
	// unbased are the URLs not read yet, to which the since rule still
	// applies.
	unbased := map[string]bool{}
	// record notes what a poll read: the first reading of an item sets
	// it aside when older than since; every waiting item's state is kept.
	record := func(res *inboxResult, items []inbox.Item) {
		unread := map[string]bool{}
		for _, u := range res.unread {
			unread[u] = true
			if first {
				unbased[u] = true
			}
		}
		for _, it := range items {
			if !it.Waiting() {
				continue
			}
			if (first || unbased[it.URL]) && !unread[it.URL] {
				delete(unbased, it.URL)
				if _, ok := last[it.URL]; !ok && !it.At.After(since) {
					last[it.URL] = it
				}
			}
		}
		first = false
	}
	fresh := func(items []inbox.Item) []inbox.Item {
		var out []inbox.Item
		for _, it := range items {
			if it.Waiting() && itemIsNews(last, it) {
				out = append(out, it)
			}
		}
		return out
	}
	remember := func(items []inbox.Item) {
		for _, it := range items {
			if it.Waiting() {
				last[it.URL] = it
			}
		}
	}
	// read collects once; answered is only called once every request of
	// the poll has succeeded, so that the stream and PollNotify agree on
	// whether GitLab answers (a forced read that fails keeps the outage).
	timedOut, err := rt.PollNotify(ctx, w.interval, w.timeout, func() (bool, error) {
		rt.ResetWarnings()
		res, items, err := collectInbox(ctx, rt, f)
		if err != nil {
			return false, err
		}
		record(res, items)
		news := fresh(items)
		if len(news) > 0 && f.cache.Kept() {
			// Read everything before printing, as inbox wait does.
			f.cache.Expire()
			rt.ResetWarnings()
			if res, items, err = collectInbox(ctx, rt, f); err != nil {
				return false, err
			}
			record(res, items)
			news = fresh(items)
		}
		st.answered()
		remember(items)
		if len(news) == 0 {
			return false, nil
		}
		if err := rt.PrintLine(inboxItemsLine{Type: "items", Items: news, CheckedAt: res.CheckedAt}, "items"); err != nil {
			return false, err
		}
		*resume = res.CheckedAt
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

// itemIsNews says whether inbox wait --follow prints it: never seen, a
// reason that the last state did not have, or a later "at".
func itemIsNews(last map[string]inbox.Item, it inbox.Item) bool {
	prev, ok := last[it.URL]
	if !ok || it.At.After(prev.At) {
		return true
	}
	for _, r := range it.Reasons {
		if !slices.Contains(prev.Reasons, r) {
			return true
		}
	}
	return false
}
