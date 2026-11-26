// Package mr reads a merge request as an agent needs it: a compact state,
// a cursor that captures that state, and the events between a cursor and
// now (pushes, human comments, resolved threads, approvals, merges).
package mr

import (
	"context"
	"net/url"
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
}

// Base returns the API path of the merge request.
func Base(r ref.MR) string {
	return gitlab.Project(r.Project) + "/merge_requests/" + strconv.Itoa(r.IID)
}

const maxDiscussionPages = 20

// Fetch reads the merge request, its approvals, its discussions and its
// versions, in parallel.
func Fetch(ctx context.Context, c *gitlab.Client, r ref.MR) (*Snapshot, error) {
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
