// Package mr reads a merge request as an agent needs it: a compact state,
// a cursor that captures that state, and the events between a cursor and
// now (pushes, human comments, resolved threads, approvals, merges).
package mr

import (
	"context"
	"net/url"
	"slices"
	"strconv"
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

// mergeParents caches the parents of merged-results commits, which never
// change.
var mergeParents sync.Map

func PipelineIsHead(ctx context.Context, c *gitlab.Client, m gitlab.MergeRequest) (bool, error) {
	hp := m.HeadPipeline
	if hp == nil {
		return false, nil
	}
	if hp.SHA == m.SHA {
		return true, nil
	}
	prefix := "refs/merge-requests/" + strconv.Itoa(m.IID) + "/"
	if hp.Ref != prefix+"merge" && hp.Ref != prefix+"train" {
		return false, nil
	}
	key := c.Host.Name + "/" + strconv.Itoa(m.ProjectID) + "/" + hp.SHA
	parents, ok := mergeParents.Load(key)
	if !ok {
		var commit struct {
			ParentIDs []string `json:"parent_ids"`
		}
		err := c.Get(ctx, "projects/"+strconv.Itoa(m.ProjectID)+"/repository/commits/"+hp.SHA, nil, &commit)
		if gitlab.IsStatus(err, 404) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		parents = commit.ParentIDs
		mergeParents.Store(key, parents)
	}
	return slices.Contains(parents.([]string), m.SHA), nil
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
	// Include are usernames that count although they look like bots, such
	// as a reviewer that runs under a service account.
	Include map[string]bool
}

// Human reports whether u is a person other than me, not ignored, and
// not a bot unless included. An author both included and ignored is
// ignored.
func (p People) Human(u gitlab.User) bool {
	if u.Username == p.Me || p.Ignore[u.Username] {
		return false
	}
	return p.Include[u.Username] || !gitlab.IsBot(u.Username)
}

// ApprovedBy lists who approved.
func (s *Snapshot) ApprovedBy() []string {
	var out []string
	for _, a := range s.Approvals.ApprovedBy {
		out = append(out, a.User.Username)
	}
	return out
}
