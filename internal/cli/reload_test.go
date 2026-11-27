package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
)

// reloadRig is a runtime whose config is a variable of the test, and a
// fake GitLab that accepts one token (in "Authorization: Bearer") and
// holds the requests it rejects until `together` of them are there: they
// all left before any 401 came back, like the requests of one burst.
type reloadRig struct {
	rt    *Runtime
	loads atomic.Int32

	mu       sync.Mutex
	token    string // the token of the config, as glab would have written it
	host     string // the default host of the config ("host:")
	fail     error  // when set, the config cannot be read
	accept   string
	rejected int
	all      chan struct{}
}

func newReloadRig(t *testing.T, together int, env map[string]string) *reloadRig {
	t.Helper()
	for _, k := range []string{"GLCLI_HOST", "GITLAB_HOST", "GLCLI_TOKEN", "GITLAB_TOKEN", "GLCLI_USER"} {
		t.Setenv(k, env[k])
	}
	rig := &reloadRig{token: "old", host: "gitlab.com", accept: "new", all: make(chan struct{})}
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if got == "" {
			got = "Bearer " + r.Header.Get("PRIVATE-TOKEN")
		}
		rig.mu.Lock()
		ok := got == "Bearer "+rig.accept
		if !ok {
			if rig.rejected++; rig.rejected == together {
				close(rig.all)
			}
		}
		rig.mu.Unlock()
		if ok {
			fmt.Fprint(w, `{"username":"me"}`)
			return
		}
		select {
		case <-rig.all:
		case <-time.After(5 * time.Second):
			t.Errorf("the %d requests did not come together", together)
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"401 Unauthorized"}`)
	}))
	t.Cleanup(gl.Close)
	rig.rt = New(&bytes.Buffer{}, &bytes.Buffer{})
	rig.rt.Debug = false
	rig.rt.LoadConfig = func() (*config.Config, error) {
		rig.loads.Add(1)
		rig.mu.Lock()
		defer rig.mu.Unlock()
		if rig.fail != nil {
			return nil, rig.fail
		}
		return &config.Config{Path: "config.yml", DefaultHost: rig.host, Hosts: map[string]config.Host{
			"gitlab.com":   {Name: "gitlab.com", APIHost: "gitlab.com", Protocol: "https", Token: rig.token, User: "me", TokenSource: "config.yml", OAuth: true},
			"code.pan.run": {Name: "code.pan.run", APIHost: "code.pan.run", Protocol: "https", Token: "other", User: "me", TokenSource: "config.yml"},
		}}, nil
	}
	rig.rt.NewClient = func(h config.Host) *gitlab.Client {
		c := gitlab.New(h)
		c.BaseURL = gl.URL
		return c
	}
	return rig
}

// get sends n requests at once and returns how many failed with a 401.
func (rig *reloadRig) get(t *testing.T, n int) (unauthorized int) {
	t.Helper()
	c, err := rig.rt.Client("")
	if err != nil {
		t.Fatal(err)
	}
	var failed atomic.Int32
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var u gitlab.User
			err := c.Get(context.Background(), "user", nil, &u)
			switch {
			case gitlab.IsStatus(err, 401):
				failed.Add(1)
			case err != nil || u.Username != "me":
				t.Errorf("got %+v, %v", u, err)
			}
		}()
	}
	wg.Wait()
	return int(failed.Load())
}

// Many requests rejected at once, while glab's config has a new token:
// the config is read again once, and every request is made again with the
// new token. (Run with -race: the goroutines share the client.)
func TestManyRejectionsOneReload(t *testing.T) {
	const n = 40
	rig := newReloadRig(t, n, nil)
	// glab renewed the token after the config was first read.
	if _, err := rig.rt.Config(); err != nil {
		t.Fatal(err)
	}
	rig.mu.Lock()
	rig.token = "new"
	rig.mu.Unlock()
	if failed := rig.get(t, n); failed != 0 {
		t.Errorf("%d of %d requests failed", failed, n)
	}
	if got := rig.loads.Load(); got != 2 {
		t.Errorf("the config was read %d times, want 2", got)
	}
	// The client keeps the new token: no more 401, no more reading.
	rig.mu.Lock()
	rejected := rig.rejected
	rig.mu.Unlock()
	if failed := rig.get(t, n); failed != 0 {
		t.Errorf("%d of %d requests failed", failed, n)
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	if rig.rejected != rejected || rig.loads.Load() != 2 {
		t.Errorf("after the renewal: %d more rejected requests, config read %d times", rig.rejected-rejected, rig.loads.Load())
	}
}

// Many requests rejected at once, while glab's config still has the same
// token: the config is read again once, and every request fails.
func TestManyRejectionsSameTokenOneReload(t *testing.T) {
	const n = 40
	rig := newReloadRig(t, n, nil)
	if failed := rig.get(t, n); failed != n {
		t.Errorf("%d of %d requests failed, want all", failed, n)
	}
	if got := rig.loads.Load(); got != 2 {
		t.Errorf("the config was read %d times, want 2", got)
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	if rig.rejected != n {
		t.Errorf("%d requests were rejected, want %d: none is made again with the same token", rig.rejected, n)
	}
}

// A later rejection, in another burst, makes the config be read again: a
// command that waits finds the new token at the poll after glab wrote it.
func TestLaterRejectionReadsTheConfigAgain(t *testing.T) {
	rig := newReloadRig(t, 1, nil)
	if failed := rig.get(t, 1); failed != 1 {
		t.Fatal("the first request must fail")
	}
	rig.mu.Lock()
	rig.token = "new"
	rig.mu.Unlock()
	if failed := rig.get(t, 1); failed != 0 {
		t.Error("the second request must be made again with the new token")
	}
	if got := rig.loads.Load(); got != 3 {
		t.Errorf("the config was read %d times, want 3", got)
	}
}

// A token from the environment is not looked for again: glab's config is
// read once.
func TestEnvTokenIsNotReloaded(t *testing.T) {
	rig := newReloadRig(t, 3, map[string]string{"GLCLI_TOKEN": "old"})
	rig.mu.Lock()
	rig.token = "new"
	rig.mu.Unlock()
	if failed := rig.get(t, 3); failed != 3 {
		t.Errorf("%d of 3 requests failed, want all", failed)
	}
	if got := rig.loads.Load(); got != 1 {
		t.Errorf("the config was read %d times, want 1", got)
	}
}

// set changes what glab's config holds, as glab or the user would.
func (rig *reloadRig) set(f func()) {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	f()
}

// The config that is read again after a 401 is only looked at for a token:
// the config the command started with still says which host is the default
// one. An "inbox wait" asks for the default host at each poll, and must not
// move to another instance because "host:" changed in glab's config.
func TestRereadConfigDoesNotChangeTheDefaultHost(t *testing.T) {
	rig := newReloadRig(t, 1, nil)
	before, err := rig.rt.Config()
	if err != nil {
		t.Fatal(err)
	}
	rig.set(func() { rig.token, rig.host = "new", "code.pan.run" })
	if failed := rig.get(t, 1); failed != 0 {
		t.Fatal("the request must be made again with the new token")
	}
	if got := rig.loads.Load(); got != 2 {
		t.Fatalf("the config was read %d times, want 2", got)
	}
	after, err := rig.rt.Config()
	if err != nil || after != before {
		t.Errorf("the config of the command changed: %p, then %p (%v)", before, after, err)
	}
	if host, err := rig.rt.DefaultHost(); err != nil || host != "gitlab.com" {
		t.Errorf("the default host is %q, want gitlab.com as when the command started (%v)", host, err)
	}
	if c, err := rig.rt.Client(""); err != nil || c.Host.Name != "gitlab.com" {
		t.Errorf("the client of the default host: %+v %v", c, err)
	}
	if got := rig.loads.Load(); got != 2 {
		t.Errorf("the config was read %d times, want 2: asking for it does not read it again", got)
	}
}

// When the config cannot be read again, the command keeps the one it
// started with, and the next 401 reads the file again.
func TestFailedRereadKeepsTheConfig(t *testing.T) {
	rig := newReloadRig(t, 1, nil)
	before, err := rig.rt.Config()
	if err != nil {
		t.Fatal(err)
	}
	rig.set(func() { rig.fail = errors.New("reading config.yml: invalid YAML near line 3") })
	if failed := rig.get(t, 1); failed != 1 {
		t.Fatal("the request must fail with the 401")
	}
	after, err := rig.rt.Config()
	if err != nil || after != before || after == nil {
		t.Errorf("the config of the command changed: %p, then %p (%v)", before, after, err)
	}
	if host, err := rig.rt.DefaultHost(); err != nil || host != "gitlab.com" {
		t.Errorf("the default host is %q (%v)", host, err)
	}
	if got := rig.loads.Load(); got != 2 {
		t.Errorf("the config was read %d times, want 2", got)
	}
	// The file is whole again, with the new token.
	rig.set(func() { rig.fail, rig.token = nil, "new" })
	if failed := rig.get(t, 1); failed != 0 {
		t.Error("the request must be made again with the new token")
	}
	if got := rig.loads.Load(); got != 3 {
		t.Errorf("the config was read %d times, want 3", got)
	}
}

// A token of the environment is for the default host of the config the
// command started with. When the file, read again, names as its default
// host the one whose token was rejected, the token of the environment is
// not sent there.
func TestRereadConfigDoesNotBringTheEnvToken(t *testing.T) {
	// 2 rejections would mean that a second token was tried.
	rig := newReloadRig(t, 1, map[string]string{"GLCLI_TOKEN": "new"})
	rig.rt.HostFlag = "code.pan.run"
	c, err := rig.rt.Client("")
	if err != nil || c.Host.Name != "code.pan.run" || c.Host.Token != "other" {
		t.Fatalf("the client: %+v %v", c, err)
	}
	rig.set(func() { rig.host = "code.pan.run" })
	var u gitlab.User
	if err := c.Get(context.Background(), "user", nil, &u); !gitlab.IsStatus(err, 401) {
		t.Errorf("got %+v %v, want the 401: the token of the environment, which GitLab would take, is not for this host", u, err)
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	if rig.rejected != 1 {
		t.Errorf("%d requests were rejected, want 1", rig.rejected)
	}
}
