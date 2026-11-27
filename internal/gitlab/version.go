package gitlab

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// serverVersions remembers the version each host gave, for as long as the
// process lives: a command that polls asks once.
var serverVersions sync.Map

// ServerVersion returns the major and minor version of the instance: 18
// and 5 for "18.5.1-ee". A failed request is not remembered, so that the
// next call asks again; a version that cannot be read is an error too.
func ServerVersion(ctx context.Context, c *Client) (major, minor int, err error) {
	v, ok := serverVersions.Load(c.Host.Name)
	if !ok {
		var out struct {
			Version string `json:"version"`
		}
		// Not GET /version, which answers the same but is deprecated.
		if err := c.Get(ctx, "metadata", nil, &out); err != nil {
			return 0, 0, err
		}
		v = out.Version
		serverVersions.Store(c.Host.Name, v)
	}
	// Whatever follows major.minor (".1-ee", ".0-pre") is left unread.
	if _, err := fmt.Sscanf(v.(string), "%d.%d", &major, &minor); err != nil {
		return 0, 0, fmt.Errorf("GitLab %s: cannot read the version %q", c.Host.Name, v)
	}
	return major, minor, nil
}

// VersionTimeout is how long GitLab is given to say its version when the
// answer only decides whether its patch IDs are trusted.
const VersionTimeout = 5 * time.Second

// versionRefused remembers the hosts that will not say their version: see
// PatchIDsTrusted.
var versionRefused sync.Map

// PatchIDsTrusted reports whether the patch IDs of this GitLab (the
// patch_id_sha of a diff version) tell a rebase from a change. They were
// computed with git patch-id --stable, which ignores whitespace, so that
// re-indenting a block kept the patch ID; Gitaly computes them with
// git patch-id --verbatim from 18.1 on (behind a feature flag in 18.0).
// They are trusted from 18.2 on: one version of margin. Any doubt (the
// version cannot be asked, or read) is false, and never an error.
//
// GitLab is given bound to answer, however much is left of ctx: a GitLab
// slow to say its version must not hold back, or time out, the command
// that asks. The version is asked once per process and host (see
// ServerVersion). So is a refusal: an answer that will not change by
// asking again (403, 404, a body that is not JSON) is remembered for as
// long as the process lives. A failure that may pass (5xx, 429, no
// answer within bound, network down) is not, and the next call asks
// again: callers that poll space their calls.
func PatchIDsTrusted(ctx context.Context, c *Client, bound time.Duration) bool {
	if _, refused := versionRefused.Load(c.Host.Name); refused {
		return false
	}
	bounded, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	major, minor, err := ServerVersion(bounded, c)
	// Not when the caller gave up: that says nothing of GitLab.
	if err != nil && ctx.Err() == nil && !IsTransient(err) {
		versionRefused.Store(c.Host.Name, true)
	}
	return err == nil && (major > 18 || major == 18 && minor >= 2)
}
