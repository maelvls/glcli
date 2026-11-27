package mr

import (
	"context"
	"strconv"
	"sync"

	"github.com/maelvls/glcli/internal/gitlab"
)

// patchIDs caches what the compare API answered for base..head, per host
// and project: commits never change, so asking again about the same two
// versions at the next poll costs nothing.
var patchIDs sync.Map

// SamePatch reports whether two diff versions of a merge request carry
// the same changes: one is a rebase (or a reword) of the other.
//
// known is false when it cannot be told: a diff is too large, collapsed
// or binary, or a commit is gone. A failure of the compare API is
// returned, and nothing is remembered of it: the next call asks again.
func SamePatch(ctx context.Context, c *gitlab.Client, projectID int, a, b gitlab.Version) (same, known bool, err error) {
	ha, err := versionPatch(ctx, c, projectID, a)
	if err != nil || !ha.known {
		return false, false, err
	}
	hb, err := versionPatch(ctx, c, projectID, b)
	if err != nil || !hb.known {
		return false, false, err
	}
	return ha.sum == hb.sum, true, nil
}

func versionPatch(ctx context.Context, c *gitlab.Client, projectID int, v gitlab.Version) (patchHash, error) {
	key := c.Host.Name + "/" + strconv.Itoa(projectID) + "/" + v.BaseCommitSHA + ".." + v.HeadCommitSHA
	if h, ok := patchIDs.Load(key); ok {
		return h.(patchHash), nil
	}
	h, err := comparePatch(ctx, c, projectID, v.BaseCommitSHA, v.HeadCommitSHA)
	if err != nil {
		if gitlab.IsStatus(err, 404) {
			return patchHash{}, nil // a commit is gone; not cached, like a failure
		}
		return patchHash{}, err
	}
	// A diff GitLab finds too large is cached too: it will not shrink.
	patchIDs.Store(key, h)
	return h, nil
}
