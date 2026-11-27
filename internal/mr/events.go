package mr

import (
	"context"
	"errors"
	"net"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maelvls/glcli/internal/gitlab"
)

// Event is one thing that happened to a merge request since a cursor.
type Event struct {
	// Kind is one of push, comment, resolved, unresolved, approved,
	// unapproved, state, pipeline.
	Kind string     `json:"kind"`
	At   *time.Time `json:"at,omitempty"`
	// By is who did it, when known.
	By string `json:"by,omitempty"`
	// Mine is true when I did it (my push, my approval, my resolve, my
	// merge, my close).
	Mine bool `json:"mine,omitempty"`

	// push
	SHA         string `json:"sha,omitempty"`
	PreviousSHA string `json:"previous_sha,omitempty"`
	// RebaseOnly is true when the changes of the merge request are the
	// same as before the push (a rebase, or a reword), false when they
	// differ, and absent when it could not be told (no patch ID from
	// GitLab and a diff too large, a binary file, a commit that is gone):
	// absent counts as a change.
	RebaseOnly *bool        `json:"rebase_only,omitempty"`
	BaseMoved  bool         `json:"base_moved,omitempty"`
	Commits    []CommitView `json:"commits,omitempty"`
	CompareURL string       `json:"compare_url,omitempty"`

	// comment
	Comment *CommentView `json:"comment,omitempty"`
	// resolved, unresolved
	Thread *ThreadRef `json:"thread,omitempty"`
	// state
	State         string `json:"state,omitempty"`
	PreviousState string `json:"previous_state,omitempty"`
	// pipeline
	Pipeline       *PipelineView `json:"pipeline,omitempty"`
	PreviousStatus string        `json:"previous_status,omitempty"`
}

// CommitView is a commit of a push.
type CommitView struct {
	SHA    string `json:"sha"`
	Title  string `json:"title"`
	Author string `json:"author,omitempty"`
}

// CommentView is a new human comment.
type CommentView struct {
	NoteID       int    `json:"note_id"`
	DiscussionID string `json:"discussion_id"`
	Body         string `json:"body"`
	URL          string `json:"url"`
	File         string `json:"file,omitempty"`
	Line         int    `json:"line,omitempty"`
	// InThread is set when the comment is a reply: the thread it answers.
	InThread *ThreadRef `json:"in_thread,omitempty"`
}

// ThreadRef identifies a thread by its first note.
type ThreadRef struct {
	DiscussionID string `json:"discussion_id"`
	StartedBy    string `json:"started_by"`
	// Mine is true when I started the thread.
	Mine      bool   `json:"mine,omitempty"`
	FirstNote string `json:"first_note"`
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
	Resolved  bool   `json:"resolved,omitempty"`
	URL       string `json:"url"`
}

// Trigger names the kinds of event an agent can wait for.
func (e Event) Trigger() string {
	switch e.Kind {
	case "push":
		if e.RebaseOnly != nil && *e.RebaseOnly {
			return "rebase"
		}
		return "push"
	case "resolved", "unresolved":
		return "thread"
	case "approved", "unapproved":
		return "approval"
	}
	return e.Kind
}

// Triggers are the valid --on values.
var Triggers = []string{"push", "rebase", "comment", "thread", "approval", "state", "pipeline"}

// DefaultTriggers is --on's default: what someone else did that calls for
// a look. Rebase-only pushes and pipelines are left out.
var DefaultTriggers = []string{"push", "comment", "thread", "approval", "state"}

// Differ computes the events between a cursor and a snapshot. It caches
// what it asks the compare API (the hash of a diff, whether two commits
// have the same tree, the commits of a push), so polling is cheap: a
// wait meets the same pending push at every poll. It is not safe for
// concurrent use.
type Differ struct {
	Client       *gitlab.Client
	People       People
	MaxBodyChars int
	// hashes are the answers of hash, and trees those of sameTree, by
	// "from..to".
	hashes map[string]patchHash
	trees  map[string]*bool
	// commits are the answers of newCommits, by "from..to".
	commits map[string][]CommitView
	// awaited counts, per diff version, the polls that waited for
	// GitLab to compute its patch ID: see AwaitsPatchID.
	awaited map[int]int
	// untrustedAt is when GitLab's patch IDs were last found not to be
	// trusted: see patchIDsTrusted.
	untrustedAt time.Time
	// now is time.Now, except in tests.
	now func() time.Time
}

type patchHash struct {
	sum   string
	known bool
}

// Events lists what happened between prev and s, oldest first.
func (d *Differ) Events(ctx context.Context, prev Cursor, s *Snapshot) ([]Event, error) {
	var events []Event
	m := s.MR
	threads := map[string]*gitlab.Discussion{}
	for i := range s.Discussions {
		threads[shortID(s.Discussions[i].ID)] = &s.Discussions[i]
	}

	// Pushes, and who pushed (from the "added N commits" system note).
	if m.SHA != prev.SHA {
		e := Event{Kind: "push", SHA: m.SHA, PreviousSHA: prev.SHA}
		if len(s.Versions) > 0 {
			v := s.Versions[0]
			t := v.CreatedAt
			e.At = &t
			if prev.Version != 0 && prev.Version != v.ID {
				e.CompareURL = m.WebURL + "/diffs?diff_id=" + strconv.Itoa(v.ID) + "&start_sha=" + prev.SHA
			}
			if prev.Base != "" {
				e.BaseMoved = prev.Base != v.BaseCommitSHA
			}
			// A version that is not the head's (a push landed while
			// fetching) says nothing about this push.
			if v.HeadCommitSHA == m.SHA && !s.Unsettled {
				same, err := d.sameChanges(ctx, prev, m.ProjectID, v)
				if err != nil {
					return nil, err
				}
				e.RebaseOnly = same
			}
		}
		if e.RebaseOnly == nil || !*e.RebaseOnly {
			// After a rebase every commit is new; from the old head, the
			// list would also hold the target branch's commits.
			from := prev.SHA
			if e.BaseMoved && len(s.Versions) > 0 {
				from = s.Versions[0].BaseCommitSHA
			}
			commits, err := d.newCommits(ctx, s, from)
			if err == nil {
				e.Commits = commits
			}
		}
		for _, dsc := range s.Discussions {
			for _, n := range dsc.Notes {
				if n.ID > prev.MaxNote && IsPushNote(n) {
					e.By, e.Mine = n.Author.Username, n.Author.Username == d.People.Me
				}
			}
		}
		events = append(events, e)
	}

	// Comments by other people.
	for _, dsc := range s.Discussions {
		for i, n := range dsc.Notes {
			if n.ID <= prev.MaxNote || n.System || !d.People.Human(n.Author) {
				continue
			}
			t := n.CreatedAt
			cv := &CommentView{
				NoteID:       n.ID,
				DiscussionID: dsc.ID,
				Body:         Truncate(n.Body, d.MaxBodyChars),
				URL:          NoteURL(m.WebURL, n.ID),
			}
			cv.File, cv.Line = position(n.Position)
			if i > 0 {
				cv.InThread = d.threadRef(m.WebURL, &dsc)
			}
			events = append(events, Event{Kind: "comment", At: &t, By: n.Author.Username, Comment: cv})
		}
	}

	// Threads resolved or reopened.
	for _, id := range prev.Unresolved {
		dsc, ok := threads[id]
		if !ok || len(dsc.Notes) == 0 || !dsc.Notes[0].Resolved {
			continue
		}
		e := Event{Kind: "resolved", Thread: d.threadRef(m.WebURL, dsc)}
		if rb := dsc.Notes[0].ResolvedBy; rb != nil {
			e.By, e.Mine = rb.Username, rb.Username == d.People.Me
		}
		events = append(events, e)
	}
	for i := range s.Discussions {
		dsc := &s.Discussions[i]
		first := dsc.Notes
		if len(first) == 0 || !first[0].Resolvable || first[0].Resolved || first[0].ID > prev.MaxNote || slices.Contains(prev.Unresolved, shortID(dsc.ID)) {
			continue
		}
		// GitLab does not say who reopened a thread: credit the author of
		// its newest note since the cursor, if any (a reply that reopens).
		e := Event{Kind: "unresolved", Thread: d.threadRef(m.WebURL, dsc)}
		for _, n := range dsc.Notes {
			if n.ID > prev.MaxNote && !n.System {
				e.By, e.Mine = n.Author.Username, n.Author.Username == d.People.Me
			}
		}
		events = append(events, e)
	}

	// Approvals.
	now := s.ApprovedBy()
	for _, u := range now {
		if !slices.Contains(prev.Approvers, u) {
			events = append(events, Event{Kind: "approved", By: u, Mine: u == d.People.Me})
		}
	}
	for _, u := range prev.Approvers {
		if !slices.Contains(now, u) {
			events = append(events, Event{Kind: "unapproved", By: u, Mine: u == d.People.Me})
		}
	}

	// State: merged, closed, reopened. The merge request does not say who
	// reopened it, so a reopen has no "by". A merge under way ("locked")
	// is not a state: see settledState. A cursor of an older glcli may
	// say "locked".
	if state, was := settledState(m.State), settledState(prev.State); state != was {
		e := Event{Kind: "state", State: state, PreviousState: was}
		var by *gitlab.User
		switch state {
		case "merged":
			by, e.At = m.Merger(), m.MergedAt
			e.SHA = firstNonEmpty(m.SquashCommitSHA, m.MergeCommitSHA)
		case "closed":
			by, e.At = m.ClosedBy, m.ClosedAt
		}
		if by != nil {
			e.By, e.Mine = by.Username, by.Username == d.People.Me
		}
		events = append(events, e)
	}

	// Head pipeline reaching a final status.
	if hp := m.HeadPipeline; hp != nil {
		cur := strconv.Itoa(hp.ID) + ":" + hp.Status
		if cur != prev.Pipeline && Finished(hp.Status) {
			_, prevStatus, _ := strings.Cut(prev.Pipeline, ":")
			if !strings.HasPrefix(prev.Pipeline, strconv.Itoa(hp.ID)+":") {
				prevStatus = ""
			}
			e := Event{
				Kind: "pipeline", SHA: hp.SHA, PreviousStatus: prevStatus,
				Pipeline: &PipelineView{ID: hp.ID, Status: hp.Status, URL: hp.WebURL, SHA: hp.SHA, Stale: !s.PipelineIsHead},
			}
			if !hp.UpdatedAt.IsZero() {
				t := hp.UpdatedAt
				e.At = &t
			}
			events = append(events, e)
		}
	}

	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i].At, events[j].At
		if a == nil || b == nil {
			return a != nil && b == nil
		}
		return a.Before(*b)
	})
	return events, nil
}

// Finished reports whether a pipeline status is final. "manual" counts: the
// pipeline waits for someone to press a button.
func Finished(status string) bool {
	switch status {
	case "success", "failed", "canceled", "skipped", "manual":
		return true
	}
	return false
}

func (d *Differ) threadRef(mrURL string, dsc *gitlab.Discussion) *ThreadRef {
	first := dsc.Notes[0]
	t := &ThreadRef{
		DiscussionID: dsc.ID,
		StartedBy:    first.Author.Username,
		Mine:         first.Author.Username == d.People.Me,
		FirstNote:    Truncate(first.Body, 200),
		Resolved:     first.Resolved,
		URL:          NoteURL(mrURL, first.ID),
	}
	t.File, t.Line = position(first.Position)
	return t
}

func position(p *gitlab.Position) (string, int) {
	if p == nil {
		return "", 0
	}
	file := firstNonEmpty(p.NewPath, p.OldPath)
	switch {
	case p.NewLine != nil:
		return file, *p.NewLine
	case p.OldLine != nil:
		return file, *p.OldLine
	}
	return file, 0
}

// newCommits lists the commits between from and the head, the last 20 at
// most. The answer is kept: commits do not change. A failure is not, so
// that the next poll asks again, except a 404: a commit is gone.
func (d *Differ) newCommits(ctx context.Context, s *Snapshot, from string) ([]CommitView, error) {
	key := from + ".." + s.MR.SHA
	if out, ok := d.commits[key]; ok {
		return out, nil
	}
	var cmp struct {
		Commits []gitlab.Commit `json:"commits"`
	}
	q := url.Values{"from": {from}, "to": {s.MR.SHA}, "straight": {"false"}}
	err := d.Client.Get(ctx, "projects/"+strconv.Itoa(s.MR.ProjectID)+"/repository/compare", q, &cmp)
	if err != nil && !gitlab.IsStatus(err, 404) {
		return nil, err
	}
	var out []CommitView
	for _, c := range cmp.Commits {
		out = append(out, CommitView{SHA: c.ShortID, Title: c.Title, Author: c.AuthorName})
	}
	if len(out) > 20 {
		out = out[len(out)-20:]
	}
	if d.commits == nil {
		d.commits = map[string][]CommitView{}
	}
	d.commits[key] = out
	return out, nil
}

// sameChanges reports whether the changes of the merge request are the
// same at the cursor and at v, its newest diff version: nil when that
// cannot be told, which callers must take for a change. The only error
// is the end of ctx: a comparison that GitLab fails to make is "cannot
// be told", so that the push and the other events are still reported.
//
// GitLab's own patch IDs decide when the cursor carries one, v has one,
// and the instance computes them verbatim (see patchIDsTrusted).
// Different patch IDs are different changes. Equal ones are the same
// changes, with one exception: a patch ID does not cover the hunk
// headers, so a change moved to another place of its file, where the
// lines around it are the same, keeps its patch ID. When the base did
// not move, the two heads are therefore compared with each other: on
// one base, the changes are the same exactly when the trees are. When
// the base moved (a rebase), equal patch IDs stand.
//
// Else (an older GitLab, a patch ID not computed, a cursor of an older
// glcli), the two heads are compared with each other when the base did
// not move, which is exact; when it moved, or when GitLab does not give
// that comparison, the two diffs are read with the compare API and must
// be identical: see PatchID.
func (d *Differ) sameChanges(ctx context.Context, prev Cursor, projectID int, v gitlab.Version) (*bool, error) {
	baseKept := prev.Base != "" && prev.Base == v.BaseCommitSHA
	if prev.Patch != "" && v.PatchIDSHA != "" && d.patchIDsTrusted(ctx) {
		same := prev.Patch == v.PatchIDSHA
		if !same || !baseKept {
			return &same, nil
		}
		return d.sameTree(ctx, projectID, prev.SHA, v.HeadCommitSHA)
	}
	if prev.Base == "" {
		return nil, nil
	}
	if baseKept {
		if same, err := d.sameTree(ctx, projectID, prev.SHA, v.HeadCommitSHA); err != nil || same != nil {
			return same, err
		}
	}
	a, err := d.hash(ctx, projectID, prev.Base, prev.SHA)
	if err != nil || !a.known {
		return nil, err
	}
	b, err := d.hash(ctx, projectID, v.BaseCommitSHA, v.HeadCommitSHA)
	if err != nil || !b.known {
		return nil, err
	}
	same := a.sum == b.sum
	return &same, nil
}

// patchIDsTrusted is gitlab.PatchIDsTrusted, asked again at most once a
// minute while the answer is no. A yes, a version too old and a refusal
// are remembered by the gitlab package; a GitLab that fails to say its
// version is not, and a wait may poll every 5 seconds.
func (d *Differ) patchIDsTrusted(ctx context.Context) bool {
	now := time.Now
	if d.now != nil {
		now = d.now
	}
	if !d.untrustedAt.IsZero() && now().Sub(d.untrustedAt) < time.Minute {
		return false
	}
	if gitlab.PatchIDsTrusted(ctx, d.Client, gitlab.VersionTimeout) {
		return true
	}
	d.untrustedAt = now()
	return false
}

// patchIDPolls is how many polls a wait gives GitLab to compute the
// patch ID of a new diff version.
const patchIDPolls = 2

// AwaitsPatchID reports whether a wait should look again at the next
// poll instead of reporting the push it sees: the cursor has a patch ID,
// GitLab's patch IDs are trusted, and the diff version of the new head
// has none yet. GitLab computes it just after it creates the version; a
// poll in between would leave the decision to the comparison of the two
// diffs, which takes some rebases for changes, and the cursor printed
// would carry no patch ID. Each call that answers true counts as a poll:
// after patchIDPolls of them for a version it answers false, and the
// diffs decide (GitLab may never compute the patch ID).
func (d *Differ) AwaitsPatchID(ctx context.Context, prev Cursor, s *Snapshot) bool {
	if s.MR.SHA == prev.SHA || prev.Patch == "" || s.Unsettled || len(s.Versions) == 0 {
		return false
	}
	v := s.Versions[0]
	// Without changes (the base is the head) GitLab computes no patch ID.
	if v.HeadCommitSHA != s.MR.SHA || v.PatchIDSHA != "" || v.BaseCommitSHA == v.HeadCommitSHA {
		return false
	}
	if d.awaited[v.ID] >= patchIDPolls || !d.patchIDsTrusted(ctx) {
		return false
	}
	if d.awaited == nil {
		d.awaited = map[int]int{}
	}
	d.awaited[v.ID]++
	return true
}

// diffAnswer is what the compare API says of the direct diff between two
// commits.
type diffAnswer struct {
	// diffs is nil when GitLab gave none: the commits are gone, GitLab
	// fails, or the answer has no "diffs". A pointer, because an answer
	// without "diffs" is not an answer without changes.
	diffs *[]gitlab.VersionDiff
	// timeout is GitLab's compare_timeout: the diffs may be incomplete.
	timeout bool
}

// compare asks GitLab for the direct diff from..to. A failure is not an
// error but a comparison without diffs: a comparison that GitLab cannot
// make (a 500, or no answer within the client's minute, on a heavy diff)
// would otherwise fail every poll of a wait, which would never report
// the push nor anything after it. A transient failure other than a
// timeout is tried once more first. The only error is the end of ctx.
func (d *Differ) compare(ctx context.Context, projectID int, from, to string) (diffAnswer, error) {
	q := url.Values{"from": {from}, "to": {to}, "straight": {"true"}}
	for attempt := 0; ; attempt++ {
		var cmp struct {
			Diffs          *[]gitlab.VersionDiff `json:"diffs"`
			CompareTimeout bool                  `json:"compare_timeout"`
		}
		err := d.Client.Get(ctx, "projects/"+strconv.Itoa(projectID)+"/repository/compare", q, &cmp)
		switch {
		case err == nil:
			return diffAnswer{diffs: cmp.Diffs, timeout: cmp.CompareTimeout}, nil
		case ctx.Err() != nil:
			return diffAnswer{}, ctx.Err()
		case attempt == 0 && gitlab.IsTransient(err) && !timedOut(err):
			continue
		}
		return diffAnswer{}, nil
	}
}

func timedOut(err error) bool {
	var netErr net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout()
}

// hash returns the hash (see PatchID) of the changes between base and
// head. It reads them with the compare API rather than from the merge
// request's diff versions: GitLab drops the files of every version but
// the latest (state "without_files"), while it keeps the commits. The
// answer is kept, unknown included: a wait does not ask again at every
// poll for a diff that GitLab could not give.
func (d *Differ) hash(ctx context.Context, projectID int, base, head string) (patchHash, error) {
	key := base + ".." + head
	if h, ok := d.hashes[key]; ok {
		return h, nil
	}
	cmp, err := d.compare(ctx, projectID, base, head)
	if err != nil {
		return patchHash{}, err
	}
	h := patchHash{}
	if cmp.diffs != nil && !cmp.timeout {
		h.sum, h.known = PatchID(*cmp.diffs)
	}
	if d.hashes == nil {
		d.hashes = map[string]patchHash{}
	}
	d.hashes[key] = h
	return h, nil
}

// sameTree reports whether two commits have the same files: GitLab finds
// no difference between them. False as soon as it lists one, nil when it
// cannot be told (no answer, or an answer cut short that lists none).
// The answer is kept, like that of hash.
func (d *Differ) sameTree(ctx context.Context, projectID int, from, to string) (*bool, error) {
	key := from + ".." + to
	if same, ok := d.trees[key]; ok {
		return same, nil
	}
	cmp, err := d.compare(ctx, projectID, from, to)
	if err != nil {
		return nil, err
	}
	var same *bool
	switch {
	case cmp.diffs == nil:
	case len(*cmp.diffs) > 0:
		same = new(bool)
	case !cmp.timeout:
		yes := true
		same = &yes
	}
	if d.trees == nil {
		d.trees = map[string]*bool{}
	}
	d.trees[key] = same
	return same, nil
}
