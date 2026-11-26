// Package inbox finds what is waiting on the user across every project of
// a GitLab instance: review requests, replies, pushes on merge requests
// they review, comments and failed pipelines on their own merge requests,
// and their pending to-do items.
package inbox

import (
	"context"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maelvls/glcli/internal/gitlab"
	"github.com/maelvls/glcli/internal/mr"
	"github.com/maelvls/glcli/internal/ref"
)

// Item is one thing that may wait on the user.
type Item struct {
	// Kind is review (I am a reviewer), authored (I opened it) or todo
	// (a to-do item about something else).
	Kind string `json:"kind"`
	// Reasons say why it waits on me: review_requested, new_commits,
	// new_replies (in my threads, or mentioning me), new_comments (on my
	// merge request), pipeline_failed, ready_to_merge, and
	// todo:<action> (todo:mentioned, todo:directly_addressed, ...).
	Reasons []string `json:"reasons,omitempty"`
	Ref     string   `json:"ref"`
	Title   string   `json:"title"`
	URL     string   `json:"url"`
	Author  string   `json:"author,omitempty"`
	// At is when the newest reason happened.
	At           time.Time `json:"at"`
	Draft        bool      `json:"draft,omitempty"`
	ApprovedByMe bool      `json:"approved_by_me,omitempty"`
	Unresolved   int       `json:"unresolved_threads,omitempty"`
	Pipeline     string    `json:"pipeline,omitempty"`
	MergeStatus  string    `json:"merge_status,omitempty"`
	TodoIDs      []int     `json:"todo_ids,omitempty"`
	// Body is the text of the to-do item (the comment that mentioned me).
	Body string `json:"body,omitempty"`
	// myLast is my last comment, approval or push on the merge request:
	// a to-do item older than that is stale.
	myLast time.Time
}

// Waiting reports whether the item needs the user.
func (it Item) Waiting() bool { return len(it.Reasons) > 0 }

// Roles select the sources.
type Roles struct{ Review, Authored, Todo bool }

// Options tune Collect.
type Options struct {
	Host         string
	People       mr.People
	Roles        Roles
	MaxBodyChars int
}

// Result is what Collect found.
type Result struct {
	Items    []Item
	Warnings []string
}

// Collect lists the open merge requests I review or authored, and my
// pending to-do items, with the reasons each waits on me.
func Collect(ctx context.Context, c *gitlab.Client, o Options) (*Result, error) {
	res := &Result{}
	me := o.People.Me
	type mrRole struct {
		m    gitlab.MergeRequest
		kind string
	}
	var mrs []mrRole
	seen := map[int]bool{}
	list := func(kind string, q url.Values) error {
		q.Set("state", "opened")
		q.Set("scope", "all")
		items, truncated, err := gitlab.GetAll[gitlab.MergeRequest](ctx, c, "merge_requests", q, 3)
		if err != nil {
			return err
		}
		if truncated {
			res.Warnings = append(res.Warnings, "more than 300 "+kind+" merge requests: only the 300 most recent were looked at")
		}
		for _, m := range items {
			if !seen[m.ID] {
				seen[m.ID] = true
				k := kind
				if m.Author.Username == me {
					k = "authored" // also my own reviewer: still my merge request
				}
				mrs = append(mrs, mrRole{m, k})
			}
		}
		return nil
	}
	if o.Roles.Review {
		if err := list("review", url.Values{"reviewer_username": {me}}); err != nil {
			return nil, err
		}
	}
	if o.Roles.Authored {
		if err := list("authored", url.Values{"author_username": {me}}); err != nil {
			return nil, err
		}
	}

	items := make([]Item, len(mrs))
	errs := make([]error, len(mrs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i, x := range mrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			project := projectOf(x.m)
			s, err := mr.Fetch(ctx, c, ref.MR{Host: o.Host, Project: project, IID: x.m.IID})
			if err != nil {
				errs[i] = err
				return
			}
			if x.kind == "review" {
				items[i] = reviewItem(s, o.People)
			} else {
				items[i] = authoredItem(s, o.People)
			}
		}()
	}
	wg.Wait()
	byURL := map[string]int{}
	for i, err := range errs {
		if err != nil {
			res.Warnings = append(res.Warnings, mrs[i].m.WebURL+": "+err.Error())
			continue
		}
		byURL[items[i].URL] = len(res.Items)
		res.Items = append(res.Items, items[i])
	}

	if o.Roles.Todo {
		todos, truncated, err := gitlab.GetAll[gitlab.Todo](ctx, c, "todos", url.Values{"state": {"pending"}}, 2)
		if err != nil {
			return nil, err
		}
		if truncated {
			res.Warnings = append(res.Warnings, "more than 200 pending to-do items: only the 200 most recent were looked at")
		}
		for _, t := range todos {
			reason := "todo:" + t.ActionName
			target := stripFragment(firstNonEmpty(t.Target.WebURL, t.TargetURL))
			if i, ok := byURL[target]; ok {
				it := &res.Items[i]
				it.TodoIDs = append(it.TodoIDs, t.ID)
				if it.Kind != "todo" && !t.CreatedAt.After(it.myLast) {
					continue // I acted on the merge request since: stale
				}
				if !slices.Contains(it.Reasons, reason) {
					it.Reasons = append(it.Reasons, reason)
				}
				if t.CreatedAt.After(it.At) {
					it.At = t.CreatedAt
				}
				if it.Body == "" && t.ActionName != "review_requested" {
					it.Body = mr.Truncate(t.Body, o.MaxBodyChars)
				}
				continue
			}
			sep := "#"
			if t.TargetType == "MergeRequest" {
				sep = "!"
			}
			byURL[target] = len(res.Items)
			res.Items = append(res.Items, Item{
				Kind:    "todo",
				Reasons: []string{reason},
				Ref:     t.Project.PathWithNamespace + sep + strconv.Itoa(t.Target.IID),
				Title:   t.Target.Title,
				URL:     target,
				Author:  t.Author.Username,
				At:      t.CreatedAt,
				Draft:   t.Target.Draft,
				TodoIDs: []int{t.ID},
				Body:    mr.Truncate(t.Body, o.MaxBodyChars),
			})
		}
	}
	sort.SliceStable(res.Items, func(i, j int) bool { return res.Items[i].At.After(res.Items[j].At) })
	return res, nil
}

func projectOf(m gitlab.MergeRequest) string {
	if full := m.References.Full; full != "" {
		if p, _, ok := strings.Cut(full, "!"); ok {
			return p
		}
	}
	_, p := ref.ParseProject(m.WebURL)
	return p
}

func stripFragment(u string) string {
	u, _, _ = strings.Cut(u, "#")
	return u
}

// activity is what I and others did on a merge request, from its notes.
type activity struct {
	myLast      time.Time // my last comment, approval or push
	reviewReqAt time.Time // last "requested review from @me"
	repliesAt   time.Time // last human reply in my threads, or mention
	commentsAt  time.Time // last human comment by someone else
	approvedAt  time.Time // last approval, by anyone
	pushedAt    time.Time
}

func analyse(s *mr.Snapshot, p mr.People) activity {
	var a activity
	me := p.Me
	if len(s.Versions) > 0 {
		a.pushedAt = s.Versions[0].CreatedAt
	}
	for _, d := range s.Discussions {
		for _, n := range d.Notes {
			if n.Author.Username == me && (!n.System || strings.HasPrefix(n.Body, "approved this merge request") || strings.HasPrefix(n.Body, "added ")) {
				a.myLast = later(a.myLast, n.CreatedAt)
			}
			if n.System && strings.HasPrefix(n.Body, "approved this merge request") {
				a.approvedAt = later(a.approvedAt, n.CreatedAt)
			}
			if n.System && strings.Contains(n.Body, "requested review from") && mentions(n.Body, me) {
				a.reviewReqAt = later(a.reviewReqAt, n.CreatedAt)
			}
		}
	}
	for _, d := range s.Discussions {
		inMine := false
		for _, n := range d.Notes {
			if !n.System && n.Author.Username == me {
				inMine = true
			}
		}
		for _, n := range d.Notes {
			if n.System || !p.Human(n.Author) {
				continue
			}
			a.commentsAt = later(a.commentsAt, n.CreatedAt)
			if (inMine || mentions(n.Body, me)) && n.CreatedAt.After(a.myLast) {
				a.repliesAt = later(a.repliesAt, n.CreatedAt)
			}
		}
	}
	return a
}

func reviewItem(s *mr.Snapshot, p mr.People) Item {
	it := base(s, "review", p)
	a := analyse(s, p)
	it.myLast = a.myLast
	switch {
	case a.reviewReqAt.After(a.myLast):
		it.add("review_requested", a.reviewReqAt)
	case a.myLast.IsZero() && !it.ApprovedByMe:
		it.add("review_requested", s.MR.CreatedAt)
	case !it.ApprovedByMe && a.pushedAt.After(a.myLast):
		it.add("new_commits", a.pushedAt)
	}
	if !a.repliesAt.IsZero() {
		it.add("new_replies", a.repliesAt)
	}
	return it
}

func authoredItem(s *mr.Snapshot, p mr.People) Item {
	it := base(s, "authored", p)
	a := analyse(s, p)
	it.myLast = a.myLast
	if a.commentsAt.After(a.myLast) {
		it.add("new_comments", a.commentsAt)
	}
	if hp := s.MR.HeadPipeline; hp != nil && hp.Status == "failed" && s.PipelineIsHead {
		it.add("pipeline_failed", hp.UpdatedAt)
	}
	if s.MR.DetailedMergeStatus == "mergeable" && !s.MR.Draft {
		// When it became mergeable: the last approval or the head
		// pipeline finishing, not updated_at, which any edit bumps.
		at := a.approvedAt
		if hp := s.MR.HeadPipeline; hp != nil && s.PipelineIsHead {
			at = later(at, hp.UpdatedAt)
		}
		if at.IsZero() {
			at = s.MR.UpdatedAt
		}
		it.add("ready_to_merge", at)
	}
	return it
}

func base(s *mr.Snapshot, kind string, p mr.People) Item {
	v := mr.NewView(s, p, mr.ViewOptions{})
	it := Item{
		Kind:         kind,
		Ref:          v.Ref,
		Title:        v.Title,
		URL:          v.URL,
		Author:       v.Author,
		At:           v.UpdatedAt,
		Draft:        v.Draft,
		ApprovedByMe: v.Approvals.ApprovedByMe,
		Unresolved:   v.Threads.Unresolved,
		MergeStatus:  v.MergeStatus,
	}
	if v.Pipeline != nil {
		it.Pipeline = v.Pipeline.Status
	}
	return it
}

func (it *Item) add(reason string, at time.Time) {
	if len(it.Reasons) == 0 || at.After(it.At) {
		it.At = at
	}
	it.Reasons = append(it.Reasons, reason)
}

// mentions reports whether body mentions @user: not @username, but
// "@user." at the end of a sentence counts.
func mentions(body, user string) bool {
	tag := "@" + user
	for from := 0; ; {
		i := strings.Index(body[from:], tag)
		if i < 0 {
			return false
		}
		end := from + i + len(tag)
		if end == len(body) || !isUsernameChar(body[end]) ||
			(body[end] == '.' && (end+1 == len(body) || !isUsernameChar(body[end+1]))) {
			return true
		}
		from = end
	}
}

func isUsernameChar(b byte) bool {
	return b == '_' || b == '-' || b == '.' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
