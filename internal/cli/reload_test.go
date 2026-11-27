package cli

import (
	"bytes"
	"context"
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
	accept   string
	rejected int
	all      chan struct{}
}

func newReloadRig(t *testing.T, together int, env map[string]string) *reloadRig {
	t.Helper()
	for _, k := range []string{"GLCLI_HOST", "GITLAB_HOST", "GLCLI_TOKEN", "GITLAB_TOKEN", "GLCLI_USER"} {
		t.Setenv(k, env[k])
	}
	rig := &reloadRig{token: "old", accept: "new", all: make(chan struct{})}
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
		return &config.Config{Path: "config.yml", DefaultHost: "gitlab.com", Hosts: map[string]config.Host{
			"gitlab.com": {Name: "gitlab.com", APIHost: "gitlab.com", Protocol: "https", Token: rig.token, User: "me", TokenSource: "config.yml", OAuth: true},
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
