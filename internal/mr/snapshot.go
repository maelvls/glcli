// Package mr reads a merge request as an agent needs it: a compact state,
// a cursor that captures that state, and the events between a cursor and
// now (pushes, human comments, resolved threads, approvals, merges).
package mr

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/maelvls/glcli/internal/gitlab"
	"github.com/maelvls/glcli/internal/ref"
)

// Snapshot is everything fetched about a merge request at one time.
type Snapshot struct {
	Ref         ref.MR
	MR          gitlab.MergeRequest
	Approvals   gitlab.Approvals
	Discussions []gitlab.Discussion
	// DiscussionsTruncated is true when there were more than maxPages pages.
	DiscussionsTruncated bool
	// Versions are the diff versions, newest first (first page only).
	Versions []gitlab.Version
	// PipelineIsHead is true when MR.HeadPipeline ran on the head commit:
	// on MR.SHA itself, or on a merged-results or merge-train commit
	// (refs/merge-requests/<iid>/merge or /train) whose parents include it.
	PipelineIsHead bool
	// Unsettled is true when a push landed while fetching (the merge
	// request and its newest version disagree on the head) even after
	// retrying: the snapshot mixes both sides of the push.
	Unsettled bool
}

// ID identifies the merge request across projects: "project_id!iid".
func (s *Snapshot) ID() string {
	return strconv.Itoa(s.MR.ProjectID) + "!" + strconv.Itoa(s.MR.IID)
}

// Base returns the API path of the merge request.
func Base(r ref.MR) string {
	return gitlab.Project(r.Project) + "/merge_requests/" + strconv.Itoa(r.IID)
}

const maxDiscussionPages = 20

// Fetch reads the merge request, its approvals, its discussions and its
// versions, in parallel. The four reads are not atomic: when a push lands
// in between (the merge request and its newest version disagree on the
// head), it reads again, up to three times, and else marks the snapshot
// Unsettled.
func Fetch(ctx context.Context, c *gitlab.Client, r ref.MR) (*Snapshot, error) {
	var s *Snapshot
	for attempt := 0; attempt < 3; attempt++ {
		var err error
		if s, err = fetchOnce(ctx, c, r); err != nil {
			return nil, err
		}
		if len(s.Versions) == 0 || s.Versions[0].HeadCommitSHA == s.MR.SHA {
			s.Unsettled = false
			break
		}
		s.Unsettled = true
	}
	isHead, err := PipelineIsHead(ctx, c, s.MR)
	if err != nil {
		return nil, err
	}
	s.PipelineIsHead = isHead
	return s, nil
}

// mergeParents caches the merged-results commits (a mergeCommit for each),
// which never change.
var mergeParents sync.Map

type mergeCommit struct {
	parents []string
	// missing is true when GitLab answered 404: it does not keep these
	// commits forever.
	missing bool
}

// PipelineIsHead reports whether the head pipeline of m ran on its head
// commit (see PipelineOnCommit).
func PipelineIsHead(ctx context.Context, c *gitlab.Client, m gitlab.MergeRequest) (bool, error) {
	if m.HeadPipeline == nil {
		return false, nil
	}
	on, err := PipelineOnCommit(ctx, c, m, *m.HeadPipeline, m.SHA)
	return on == OnCommitYes, err
}

// OnCommit is the answer of PipelineOnCommit.
type OnCommit int

const (
	// OnCommitNo: the pipeline ran on another commit.
	OnCommitNo OnCommit = iota
	// OnCommitYes: it ran on the commit, or on a merge commit of it.
	OnCommitYes
	// OnCommitUnknown: it ran on a merged-results or merge-train commit
	// that cannot be read (HTTP 404), so its parents are not known.
	OnCommitUnknown
)

// PipelineOnCommit reports whether the pipeline p of the merge request m
// ran on the commit sha (full or abbreviated): on that commit itself, or
// on a merged-results or merge-train commit of it. The SHA of such a
// pipeline (ref refs/merge-requests/<iid>/merge or /train) is a temporary
// merge commit, which has the commit among its parents: reading them
// costs one request per pipeline, once. Only the merge commit of the head
// pipeline is asked for again when it cannot be read: it is recent, and
// may become readable.
func PipelineOnCommit(ctx context.Context, c *gitlab.Client, m gitlab.MergeRequest, p gitlab.PipelineRef, sha string) (OnCommit, error) {
	if sha == "" {
		return OnCommitNo, nil
	}
	if strings.HasPrefix(p.SHA, sha) {
		return OnCommitYes, nil
	}
	prefix := "refs/merge-requests/" + strconv.Itoa(m.IID) + "/"
	if p.Ref != prefix+"merge" && p.Ref != prefix+"train" {
		return OnCommitNo, nil
	}
	key := c.Host.Name + "/" + strconv.Itoa(m.ProjectID) + "/" + p.SHA
	cached, ok := mergeParents.Load(key)
	commit, _ := cached.(mergeCommit)
	isHead := m.HeadPipeline != nil && m.HeadPipeline.ID == p.ID
	if !ok || (commit.missing && isHead) {
		var answer struct {
			ParentIDs []string `json:"parent_ids"`
		}
		err := c.Get(ctx, "projects/"+strconv.Itoa(m.ProjectID)+"/repository/commits/"+p.SHA, nil, &answer)
		switch {
		case gitlab.IsStatus(err, 404):
			commit = mergeCommit{missing: true}
		case err != nil:
			return OnCommitNo, err
		default:
			commit = mergeCommit{parents: answer.ParentIDs}
		}
		mergeParents.Store(key, commit)
	}
	if commit.missing {
		return OnCommitUnknown, nil
	}
	if slices.ContainsFunc(commit.parents, func(parent string) bool { return strings.HasPrefix(parent, sha) }) {
		return OnCommitYes, nil
	}
	return OnCommitNo, nil
}

func fetchOnce(ctx context.Context, c *gitlab.Client, r ref.MR) (*Snapshot, error) {
	s := &Snapshot{Ref: r}
	base := Base(r)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	run := func(i int, f func() error) {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = f() }()
	}
	run(0, func() error { return c.Get(ctx, base, nil, &s.MR) })
	run(1, func() error { return c.Get(ctx, base+"/approvals", nil, &s.Approvals) })
	run(2, func() error {
		var err error
		s.Discussions, s.DiscussionsTruncated, err = gitlab.GetAll[gitlab.Discussion](ctx, c, base+"/discussions", nil, maxDiscussionPages)
		return err
	})
	run(3, func() error {
		return c.Get(ctx, base+"/versions", url.Values{"per_page": {"20"}}, &s.Versions)
	})
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

// People decides whose notes count as human comments.
type People struct {
	Me     string
	Ignore map[string]bool
}

// Human reports whether u is a person other than me, not a bot and not
// ignored.
func (p People) Human(u gitlab.User) bool {
	return u.Username != p.Me && !gitlab.IsBot(u.Username) && !p.Ignore[u.Username]
}

// ApprovedBy lists who approved.
func (s *Snapshot) ApprovedBy() []string {
	var out []string
	for _, a := range s.Approvals.ApprovedBy {
		out = append(out, a.User.Username)
	}
	return out
}
