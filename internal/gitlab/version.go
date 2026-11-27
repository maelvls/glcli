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

// PatchIDsTrusted reports whether the patch IDs of this GitLab (the
// patch_id_sha of a diff version) tell a rebase from a change. Before
// 18.2 they could be computed with git patch-id --stable, which ignores
// whitespace: re-indenting a block kept the patch ID. From 18.2 on it is
// git patch-id --verbatim. Any doubt (the version cannot be asked, or
// read) is false, and never an error.
//
// GitLab is given bound to answer, however much is left of ctx: a GitLab
// slow to say its version must not hold back, or time out, the command
// that asks. The version is asked once per process and host (see
// ServerVersion); a failed or expired request is not remembered, and is
// asked again at the next call.
func PatchIDsTrusted(ctx context.Context, c *Client, bound time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	major, minor, err := ServerVersion(ctx, c)
	return err == nil && (major > 18 || major == 18 && minor >= 2)
}
