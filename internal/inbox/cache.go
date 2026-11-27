package inbox

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/maelvls/glcli/internal/gitlab"
	"github.com/maelvls/glcli/internal/mr"
	"github.com/maelvls/glcli/internal/ref"
)

// Cache keeps what Collect read of each merge request, for the next
// Collect of the same process: inbox wait polls every two minutes, and
// most merge requests have not changed in between. It trusts the
// updated_at that the lists give for each merge request, as far as
// GitLab's code allows:
//
//   - Comments, system notes (pushes, approvals, review requests) and
//     edits change updated_at, but a note only does when the last change
//     is more than a minute old (ThrottledTouch). So what was read of a
//     merge request is only kept when it was read at least settleAfter
//     after this process first saw its updated_at: what happens later
//     changes updated_at, what happened earlier was read. Until then the
//     merge request is read again at each poll.
//   - The head pipeline, its status and the merge status change without
//     updated_at: for my own merge requests, where they are reasons, the
//     merge request itself is read at each poll (one request), and only
//     its approvals, discussions and versions are kept.
//   - GitLab removes approvals without touching the merge request, and
//     writes the note that says so later, or not at all: while I am among
//     the approvers of a merge request I review, its approvals are read at
//     each poll (one request).
//   - Whatever was missed: everything is read again after maxAge, so that
//     a wrong assumption costs a delay, never a miss.
//
// What is kept for merge requests I review can thus show, for up to
// maxAge, an older pipeline status, merge status or count of unresolved
// threads: none of them is a reason.
//
// A Cache is safe for concurrent use. Errors are not kept: a merge
// request that could not be read is read again in full the next time.
type Cache struct {
	now func() time.Time

	mu      sync.Mutex
	entries map[cacheKey]*cacheEntry
}

const (
	// settleAfter is GitLab's ThrottledTouch::TOUCH_INTERVAL (one minute),
	// and thirty seconds for what GitLab does after the change (workers)
	// and for the clocks of its servers. It is less than the default
	// interval of inbox wait, whose second poll then settles.
	settleAfter = 90 * time.Second
	// maxAge is ten polls of inbox wait at its default interval: the
	// requests are still divided by ten, and what the reasoning above
	// missed is late by twenty minutes at most.
	maxAge = 20 * time.Minute
)

// NewCache returns an empty cache that reads the time with now.
func NewCache(now func() time.Time) *Cache {
	return &Cache{now: now, entries: map[cacheKey]*cacheEntry{}}
}

type cacheKey struct {
	host         string
	project, iid int
}

func keyOf(host string, m gitlab.MergeRequest) cacheKey {
	return cacheKey{host, m.ProjectID, m.IID}
}

type cacheEntry struct {
	// updatedAt is the updated_at the list gave, not the one read with the
	// merge request afterwards: a change between the two is then seen as
	// a change by the next poll.
	updatedAt time.Time
	// firstSeen is when this process first saw updatedAt in a list.
	firstSeen time.Time
	// snap is the last full read, nil when it failed.
	snap   *mr.Snapshot
	readAt time.Time
	// settled is true when snap was read at least settleAfter after
	// firstSeen, with nothing changing while it was read.
	settled bool
}

// start is called once per Collect, after the lists were read and before
// any merge request is: it returns the time of this poll.
func (ca *Cache) start() time.Time {
	if ca == nil {
		return time.Time{}
	}
	return ca.now()
}

// snapshot returns what Fetch would, reading only what may have changed
// since the last poll. m is the merge request as the list just gave it,
// kind is review or authored.
func (ca *Cache) snapshot(ctx context.Context, c *gitlab.Client, host string, m gitlab.MergeRequest, kind, me string, now time.Time) (*mr.Snapshot, error) {
	r := ref.MR{Host: host, Project: projectOf(m), IID: m.IID}
	if ca == nil {
		return mr.Fetch(ctx, c, r)
	}
	key := keyOf(host, m)
	ca.mu.Lock()
	e := ca.entries[key]
	if e == nil || !e.updatedAt.Equal(m.UpdatedAt) {
		e = &cacheEntry{updatedAt: m.UpdatedAt, firstSeen: now}
		ca.entries[key] = e
	}
	// The wall clock for the age: a Mac that slept must not find
	// everything young.
	age := now.Round(0).Sub(e.readAt.Round(0))
	kept := e.snap
	if !e.settled || age < 0 || age >= maxAge {
		kept = nil
	}
	firstSeen := e.firstSeen
	ca.mu.Unlock()

	if kept != nil {
		s, err := refresh(ctx, c, r, kept, m, kind, me)
		if err != nil {
			ca.store(e, nil, now, false)
			return nil, err
		}
		if s != nil {
			return s, nil
		}
	}
	s, err := mr.Fetch(ctx, c, r)
	if err != nil {
		ca.store(e, nil, now, false)
		return nil, err
	}
	settled := !m.UpdatedAt.IsZero() && !s.Unsettled && now.Sub(firstSeen) >= settleAfter
	ca.store(e, s, now, settled)
	return s, nil
}

func (ca *Cache) store(e *cacheEntry, s *mr.Snapshot, now time.Time, settled bool) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	e.snap, e.readAt, e.settled = s, now, settled
}

// refresh reads what changes without updated_at and matters to an item
// of this kind, and returns kept with it. It returns nil when what it
// read shows that kept is out of date: everything must be read again.
func refresh(ctx context.Context, c *gitlab.Client, r ref.MR, kept *mr.Snapshot, listed gitlab.MergeRequest, kind, me string) (*mr.Snapshot, error) {
	if kind != "authored" {
		if !slices.Contains(kept.ApprovedBy(), me) {
			return kept, nil
		}
		var approvals gitlab.Approvals
		if err := c.Get(ctx, mr.Base(r)+"/approvals", nil, &approvals); err != nil {
			return nil, err
		}
		s := *kept
		s.Approvals = approvals
		if !slices.Equal(s.ApprovedBy(), kept.ApprovedBy()) {
			return nil, nil
		}
		return kept, nil
	}
	s := *kept
	s.MR = gitlab.MergeRequest{}
	if err := c.Get(ctx, mr.Base(r), nil, &s.MR); err != nil {
		return nil, err
	}
	if !s.MR.UpdatedAt.Equal(listed.UpdatedAt) || s.MR.SHA != kept.MR.SHA {
		return nil, nil
	}
	isHead, err := mr.PipelineIsHead(ctx, c, s.MR)
	if err != nil {
		return nil, err
	}
	s.PipelineIsHead = isHead
	return &s, nil
}

// keep forgets the merge requests of host that are not among listed:
// merged, closed, or no longer mine to review.
func (ca *Cache) keep(host string, listed []gitlab.MergeRequest) {
	if ca == nil {
		return
	}
	still := map[cacheKey]bool{}
	for _, m := range listed {
		still[keyOf(host, m)] = true
	}
	ca.mu.Lock()
	defer ca.mu.Unlock()
	for key := range ca.entries {
		if key.host == host && !still[key] {
			delete(ca.entries, key)
		}
	}
}
