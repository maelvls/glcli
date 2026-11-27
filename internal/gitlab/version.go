package gitlab

import (
	"context"
	"fmt"
	"sync"
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
