package mr

import (
	"context"
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
	// Mine is true when I did it (my push, my approval, my resolve).
	Mine bool `json:"mine,omitempty"`

	// push
	SHA         string `json:"sha,omitempty"`
	PreviousSHA string `json:"previous_sha,omitempty"`
	// RebaseOnly is true when the changes of the merge request are the
	// same as before the push (a rebase, or a reword), false when they
	// differ, and absent when it could not be told (diff too large).
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
// the patch hash of each diff version, so polling is cheap.
type Differ struct {
	Client       *gitlab.Client
	People       People
	MaxBodyChars int
	hashes       map[string]patchHash
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
				a, err := d.hash(ctx, s.MR.ProjectID, prev.Base, prev.SHA)
				if err != nil {
					return nil, err
				}
				b, err := d.hash(ctx, s.MR.ProjectID, v.BaseCommitSHA, v.HeadCommitSHA)
				if err != nil {
					return nil, err
				}
				if a.known && b.known {
					same := a.sum == b.sum
					e.RebaseOnly = &same
				}
			}
		}
		if e.RebaseOnly == nil || !*e.RebaseOnly {
			commits, err := d.newCommits(ctx, s, prev.SHA)
			if err == nil {
				e.Commits = commits
			}
		}
		for _, dsc := range s.Discussions {
			for _, n := range dsc.Notes {
				if n.ID > prev.MaxNote && n.System && strings.HasPrefix(n.Body, "added ") && strings.Contains(n.Body, "commit") {
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
	for id, dsc := range threads {
		first := dsc.Notes
		if len(first) == 0 || !first[0].Resolvable || first[0].Resolved || first[0].ID > prev.MaxNote || slices.Contains(prev.Unresolved, id) {
			continue
		}
		events = append(events, Event{Kind: "unresolved", Thread: d.threadRef(m.WebURL, dsc)})
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

	// State: merged, closed, reopened.
	if m.State != prev.State {
		e := Event{Kind: "state", State: m.State, PreviousState: prev.State}
		switch {
		case m.State == "merged" && m.MergedBy != nil:
			e.By, e.Mine, e.At = m.MergedBy.Username, m.MergedBy.Username == d.People.Me, m.MergedAt
			e.SHA = firstNonEmpty(m.SquashCommitSHA, m.MergeCommitSHA)
		case m.State == "closed":
			e.At = m.ClosedAt
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
				Pipeline: &PipelineView{ID: hp.ID, Status: hp.Status, URL: hp.WebURL, SHA: hp.SHA, Stale: hp.SHA != m.SHA},
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

func (d *Differ) newCommits(ctx context.Context, s *Snapshot, from string) ([]CommitView, error) {
	var cmp struct {
		Commits []gitlab.Commit `json:"commits"`
	}
	q := url.Values{"from": {from}, "to": {s.MR.SHA}, "straight": {"false"}}
	if err := d.Client.Get(ctx, "projects/"+strconv.Itoa(s.MR.ProjectID)+"/repository/compare", q, &cmp); err != nil {
		return nil, err
	}
	var out []CommitView
	for _, c := range cmp.Commits {
		out = append(out, CommitView{SHA: c.ShortID, Title: c.Title, Author: c.AuthorName})
	}
	if len(out) > 20 {
		out = out[len(out)-20:]
	}
	return out, nil
}

// hash returns the patch ID of the changes between base and head. It
// reads them with the compare API rather than from the merge request's
// diff versions: GitLab drops the files of every version but the latest
// (state "without_files"), while it keeps the commits.
func (d *Differ) hash(ctx context.Context, projectID int, base, head string) (patchHash, error) {
	key := base + ".." + head
	if h, ok := d.hashes[key]; ok {
		return h, nil
	}
	var cmp struct {
		Diffs          []gitlab.VersionDiff `json:"diffs"`
		CompareTimeout bool                 `json:"compare_timeout"`
	}
	q := url.Values{"from": {base}, "to": {head}, "straight": {"true"}}
	if err := d.Client.Get(ctx, "projects/"+strconv.Itoa(projectID)+"/repository/compare", q, &cmp); err != nil {
		if gitlab.IsStatus(err, 404) {
			return patchHash{}, nil // a commit is gone
		}
		return patchHash{}, err
	}
	h := patchHash{}
	if !cmp.CompareTimeout {
		h.sum, h.known = PatchID(cmp.Diffs)
	}
	if d.hashes == nil {
		d.hashes = map[string]patchHash{}
	}
	d.hashes[key] = h
	return h, nil
}
