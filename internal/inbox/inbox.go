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
	// myLast is my last comment, review (approval, unapproval, requested
	// changes) or push on the merge request: a to-do item older than that
	// is stale.
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
	// Cache, when set, is where a process that calls Collect again and
	// again (inbox wait) keeps what it read of the merge requests that
	// have not changed. Without it, everything is read each time.
	Cache *Cache
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

	// After the lists, before the merge requests: see Cache.
	now := o.Cache.start()
	// Asked at most once, and only if a merge request needs the answer.
	trusted := sync.OnceValue(func() bool { return patchIDsTrusted(ctx, c) })
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
			s, err := o.Cache.snapshot(ctx, c, o.Host, x.m, x.kind, me, now)
			if err != nil {
				errs[i] = err
				return
			}
			if x.kind == "review" {
				items[i] = reviewItemWith(s, o.People, trusted)
			} else {
				items[i] = authoredItem(s, o.People)
			}
		}()
	}
	wg.Wait()
	listed := make([]gitlab.MergeRequest, len(mrs))
	for i, x := range mrs {
		listed[i] = x.m
	}
	o.Cache.keep(o.Host, listed)
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
				// Without a reason, the item is dated from its last
				// update, which is not something that waits on me.
				noReason := len(it.Reasons) == 0
				if !slices.Contains(it.Reasons, reason) {
					it.Reasons = append(it.Reasons, reason)
				}
				if noReason || t.CreatedAt.After(it.At) {
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
	// myReview is my last review action: a comment or a review note
	// (isReviewNote). A push of mine is not one: who only rebased still
	// owes the review.
	myReview time.Time
	// myLast is my last action of any kind: myReview or a push of mine.
	myLast time.Time
	// seenAt is until when I know the changes: myReview, or the pushes of
	// mine that followed it with no push of someone else in between. A
	// push of mine on top of changes I did not review does not make them
	// seen.
	seenAt      time.Time
	reviewReqAt time.Time // last "requested review from @me"
	repliesAt   time.Time // last human reply in my threads, or mention
	commentsAt  time.Time // last human comment by someone else
	approvedAt  time.Time // last approval, by anyone
	// pushedAt is the newest push that is not mine: I do not review my
	// own pushes, and they must not date what waits on me.
	pushedAt time.Time
	// mine says, for each diff version (as in Snapshot.Versions, newest
	// first), whether I pushed it.
	mine []bool
}

func analyse(s *mr.Snapshot, p mr.People) activity {
	var a activity
	me := p.Me
	var myPushes []time.Time
	for _, d := range s.Discussions {
		for _, n := range d.Notes {
			if n.Author.Username == me && (!n.System || isReviewNote(n)) {
				a.myReview = later(a.myReview, n.CreatedAt)
			}
			if n.Author.Username == me && mr.IsPushNote(n) {
				myPushes = append(myPushes, n.CreatedAt)
			}
			if n.System && strings.HasPrefix(n.Body, "approved this merge request") {
				a.approvedAt = later(a.approvedAt, n.CreatedAt)
			}
			// Not when the note is mine: GitLab writes it under my name
			// when I review without being a reviewer, before the note of
			// the review (UpdateReviewerStateService#resolve_reviewer), and
			// the "approved" note comes later, from a worker.
			if n.System && n.Author.Username != me && strings.Contains(n.Body, "requested review from") && mentions(n.Body, me) {
				a.reviewReqAt = later(a.reviewReqAt, n.CreatedAt)
			}
		}
	}
	a.myLast, a.seenAt = a.myReview, a.myReview
	// Oldest first. GitLab creates the diff version of a push, then writes
	// its "added N commits" note under who pushed
	// (MergeRequests::RefreshService): a version is mine when a push note
	// of mine falls between it and the next one. A push whose note is
	// missing counts as someone else's.
	foreign := false
	a.mine = make([]bool, len(s.Versions))
	for i := len(s.Versions) - 1; i >= 0; i-- {
		v := s.Versions[i]
		var mine time.Time
		for _, t := range myPushes {
			if !t.Before(v.CreatedAt) && (i == 0 || t.Before(s.Versions[i-1].CreatedAt)) {
				mine = later(mine, t)
			}
		}
		a.mine[i] = !mine.IsZero()
		if mine.IsZero() {
			a.pushedAt = v.CreatedAt
			foreign = foreign || v.CreatedAt.After(a.myReview)
		} else if !foreign {
			a.seenAt = later(a.seenAt, mine)
		}
	}
	for _, t := range myPushes {
		a.myLast = later(a.myLast, t)
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

// isReviewNote reports whether n is one of the system notes GitLab writes
// under the name of who reviews, comment or not: "approved this merge
// request", "unapproved this merge request", "requested changes" (a
// review submitted with "Request changes") and "left review comments" (a
// review submitted with "Comment"). Their author acted on the merge
// request (SystemNotes::MergeRequestsService).
func isReviewNote(n gitlab.Note) bool {
	if !n.System {
		return false
	}
	for _, start := range []string{"approved this merge request", "unapproved this merge request", "requested changes", "left review comments"} {
		if strings.HasPrefix(n.Body, start) {
			return true
		}
	}
	return false
}

// reviewItem is reviewItemWith on a GitLab whose patch IDs are not
// trusted: every push of someone else after what I last saw counts as new
// commits.
func reviewItem(s *mr.Snapshot, p mr.People) Item {
	return reviewItemWith(s, p, func() bool { return false })
}

// reviewItemWith tells a rebase from a change with GitLab's patch IDs
// when trusted says they can be (see patchIDsTrusted). trusted is only
// asked when its answer matters.
func reviewItemWith(s *mr.Snapshot, p mr.People, trusted func() bool) Item {
	it := base(s, "review", p)
	a := analyse(s, p)
	it.myLast = a.myLast
	switch {
	case a.reviewReqAt.After(a.myReview):
		it.add("review_requested", a.reviewReqAt)
	case a.myReview.IsZero() && !it.ApprovedByMe:
		it.add("review_requested", s.MR.CreatedAt)
	case !it.ApprovedByMe && a.pushedAt.After(a.seenAt):
		if at, unseen := unseenPush(s, p.Me, a, trusted); unseen {
			it.add("new_commits", at)
		}
	}
	if !a.repliesAt.IsZero() {
		it.add("new_replies", a.repliesAt)
	}
	return it
}

// patchIDsTrusted reports whether the patch IDs of this GitLab tell a
// rebase from a change: the rule, GitLab 18.2 or newer, is
// gitlab.PatchIDsTrusted, which mr wait follows too. Any doubt (the
// version cannot be asked, or read) is false, and never an error.
func patchIDsTrusted(ctx context.Context, c *gitlab.Client) bool {
	// Every merge request that needs the answer waits for it: a GitLab
	// slow to give its version must not make the inbox time out.
	return gitlab.PatchIDsTrusted(ctx, c, versionTimeout)
}

// versionTimeout is how long GitLab is given to say its version. A
// variable for the tests.
var versionTimeout = gitlab.VersionTimeout

// unseenPush reports whether the pushes since what I last saw (a.seenAt)
// left me something to look at, and since when. They did not when the
// diff version I last saw and the newest one have the same patch ID, as
// computed by GitLab. Any doubt keeps them, dated like any push that is
// not mine: a push landed while fetching, notes are missing, a patch ID
// is missing or not trusted, what I last saw is older than every version
// fetched.
//
// The caller has checked that I am not among the approvers, and that
// someone else pushed since what I last saw.
func unseenPush(s *mr.Snapshot, me string, a activity, trusted func() bool) (at time.Time, unseen bool) {
	if s.Unsettled || s.DiscussionsTruncated || len(s.Versions) == 0 || s.Versions[0].PatchIDSHA == "" {
		return a.pushedAt, true
	}
	newest := s.Versions[0]
	// Versions are newest first: the first one not after what I last saw
	// is the one whose patch I know.
	seen := slices.IndexFunc(s.Versions, func(v gitlab.Version) bool { return !v.CreatedAt.After(a.seenAt) })
	// same: the newest patch is one I know. I know the one I saw, and the
	// one of a push of mine made while the patch was one I knew: after a
	// rebase by someone else that kept it, or after a change undone since.
	// (Not for analyse to say: without trusted patch IDs, that rebase
	// counts.) Only for the patch: the dates and the doubts stay what they
	// were before my push.
	same, known := seen >= 0, ""
	if same {
		known = s.Versions[seen].PatchIDSHA
	}
	for i := seen - 1; i >= 0; i-- {
		switch v := s.Versions[i]; {
		case v.PatchIDSHA != "" && v.PatchIDSHA == known:
			same = true
		case a.mine[i] && same:
			known = v.PatchIDSHA
		default:
			same = false
		}
	}
	// The changes arrived with the oldest of the pushes since what I last
	// saw that all have the newest patch ID. The rebases that followed
	// must not move the date, or inbox wait would fire for each of them.
	first := 0
	for i := 1; i < len(s.Versions) && s.Versions[i].PatchIDSHA == newest.PatchIDSHA && s.Versions[i].CreatedAt.After(a.seenAt); i++ {
		first = i
	}
	// Not the same, and no older push with the newest patch: the changes
	// came with the newest push, patch IDs trusted or not, unless it is
	// mine.
	if (!same && first == 0 && !a.mine[0]) || !trusted() {
		return a.pushedAt, true
	}
	// Not from a push of mine either: the changes of someone else that I
	// have not seen came before it. From the newest push before it that
	// is not mine, then, and the oldest one with that patch ID.
	for a.mine[first] {
		next := first + 1
		for next < len(s.Versions) && a.mine[next] {
			next++
		}
		if next == len(s.Versions) || !s.Versions[next].CreatedAt.After(a.seenAt) {
			break
		}
		first = next
		for i := first + 1; i < len(s.Versions) && s.Versions[i].PatchIDSHA == s.Versions[first].PatchIDSHA && s.Versions[i].CreatedAt.After(a.seenAt); i++ {
			first = i
		}
	}
	at = s.Versions[first].CreatedAt
	if !same {
		return at, true
	}
	// The changes are the ones I saw: only a doubt keeps the push. With
	// both doubts, the later date: the push that took my approval away,
	// or the retarget.
	retargeted, approvalGone := doubts(s, me, a.seenAt, a.mine)
	if retargeted.IsZero() && !approvalGone {
		return time.Time{}, false
	}
	return later(at, retargeted), true
}

// doubts reads in the system notes what an unchanged patch ID does not
// say: when the merge request was last retargeted since what I last saw
// (the same changes now go to another branch), and whether GitLab took my
// approval away. mine says which diff versions I pushed.
func doubts(s *mr.Snapshot, me string, seenAt time.Time, mine []bool) (retargeted time.Time, approvalGone bool) {
	// aware is the last time, not after what I last saw, that my review
	// was requested or the merge request retargeted: both can remove my
	// approval without a note of its own.
	var approved, withdrawn, aware time.Time
	for _, d := range s.Discussions {
		for _, n := range d.Notes {
			if !n.System {
				continue
			}
			if strings.HasPrefix(n.Body, "changed target branch") || strings.Contains(n.Body, "This merge request now targets") {
				if n.CreatedAt.After(seenAt) {
					retargeted = later(retargeted, n.CreatedAt)
				} else {
					aware = later(aware, n.CreatedAt)
				}
			}
			if !n.CreatedAt.After(seenAt) && strings.Contains(n.Body, "requested review from") && mentions(n.Body, me) {
				aware = later(aware, n.CreatedAt)
			}
			if n.Author.Username != me {
				continue
			}
			switch {
			case strings.HasPrefix(n.Body, "approved this merge request"):
				approved = later(approved, n.CreatedAt)
			case strings.HasPrefix(n.Body, "unapproved this merge request"), strings.HasPrefix(n.Body, "requested changes"):
				withdrawn = later(withdrawn, n.CreatedAt) // requesting changes removes my approval too
			}
		}
	}
	if approved.IsZero() || withdrawn.After(approved) {
		return retargeted, false
	}
	// I approved and did not take it back, yet I am not among the
	// approvers: GitLab removed my approval. Whatever I knew of it, the
	// changes were others for a while since what I last saw (changed, then
	// back, by someone else): that may be what removed it, and I did not
	// act since.
	saw := slices.IndexFunc(s.Versions, func(v gitlab.Version) bool { return !v.CreatedAt.After(seenAt) })
	for i := 0; i < saw; i++ {
		if v := s.Versions[i]; !mine[i] && v.PatchIDSHA != "" && v.PatchIDSHA != s.Versions[saw].PatchIDSHA {
			return retargeted, true
		}
	}
	// Else I know that it is gone if, between my approval and what I last
	// saw, my review was requested again, the merge request was
	// retargeted, or a push changed the patch I approved: I acted since
	// without it.
	if aware.After(approved) {
		return retargeted, false
	}
	i := slices.IndexFunc(s.Versions, func(v gitlab.Version) bool { return !v.CreatedAt.After(approved) })
	if i < 0 || s.Versions[i].PatchIDSHA == "" {
		return retargeted, true // what I approved is not known
	}
	knew := slices.ContainsFunc(s.Versions, func(v gitlab.Version) bool {
		return v.CreatedAt.After(approved) && !v.CreatedAt.After(seenAt) && v.PatchIDSHA != "" && v.PatchIDSHA != s.Versions[i].PatchIDSHA
	})
	return retargeted, !knew
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
// "@user." at the end of a sentence counts. Case is ignored, as GitLab
// does for usernames: "@Mael.Valais" mentions mael.valais.
func mentions(body, user string) bool {
	tag := "@" + user
	for from := 0; ; {
		i := indexFoldASCII(body[from:], tag)
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

// indexFoldASCII is strings.Index ignoring the case of ASCII letters
// only: GitLab usernames are ASCII, and Unicode case folding would also
// read the Kelvin sign as "k" and "İ" as "i".
func indexFoldASCII(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		j := 0
		for j < len(sub) && lowerASCII(s[i+j]) == lowerASCII(sub[j]) {
			j++
		}
		if j == len(sub) {
			return i
		}
	}
	return -1
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
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
