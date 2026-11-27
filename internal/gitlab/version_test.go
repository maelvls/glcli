package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/config"
)

// releaseServer answers GET /api/v4/version and counts the requests.
type releaseServer struct {
	// answer is the body; with status set, it is an error.
	answer string
	status atomic.Int64
	calls  atomic.Int64
}

func (f *releaseServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v4/version" {
		http.NotFound(w, r)
		return
	}
	f.calls.Add(1)
	if status := f.status.Load(); status != 0 {
		http.Error(w, `{"message":"failed"}`, int(status))
		return
	}
	_, _ = w.Write([]byte(f.answer))
}

// releaseHosts numbers the fake hosts: what a host answered is remembered
// for as long as the process lives.
var releaseHosts atomic.Int64

func (f *releaseServer) client(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New(config.Host{Name: fmt.Sprintf("release-%d", releaseHosts.Add(1)), Token: "x"})
	c.BaseURL = srv.URL
	return c
}

func TestServerVersion(t *testing.T) {
	tests := []struct {
		answer       string
		major, minor int
		ok           bool
	}{
		// The example answer in GitLab's documentation (doc/api/metadata.md).
		{`{"version": "18.1.1-ee", "revision": "ceb07b24cb0", "kas": {"enabled": true, "version": "18.1.1"}, "enterprise": true}`, 18, 1, true},
		{`{"version": "18.5.1-ee"}`, 18, 5, true},
		{`{"version": "18.2.0-pre"}`, 18, 2, true},
		{`{"version": "17.11.4"}`, 17, 11, true},
		{`{"version": "18.2"}`, 18, 2, true},
		{`{"version": "18"}`, 0, 0, false},
		{`{"version": "main"}`, 0, 0, false},
		{`{"version": ""}`, 0, 0, false},
		{`{}`, 0, 0, false},
	}
	for _, tt := range tests {
		f := &releaseServer{answer: tt.answer}
		c := f.client(t)
		// Asked twice: the answer is remembered, readable or not.
		for range 2 {
			major, minor, err := ServerVersion(context.Background(), c)
			if major != tt.major || minor != tt.minor || (err == nil) != tt.ok {
				t.Errorf("%s: got %d.%d, %v", tt.answer, major, minor, err)
			}
		}
		if got := f.calls.Load(); got != 1 {
			t.Errorf("%s: asked %d times, want 1", tt.answer, got)
		}
	}
}

func TestServerVersionFailureIsNotRemembered(t *testing.T) {
	f := &releaseServer{answer: `{"version": "18.5.1-ee"}`}
	c := f.client(t)
	for _, status := range []int64{500, 403} {
		f.status.Store(status)
		if _, _, err := ServerVersion(context.Background(), c); !IsStatus(err, int(status)) {
			t.Errorf("GitLab answering %d: err = %v", status, err)
		}
	}
	f.status.Store(0)
	for range 2 {
		if major, minor, err := ServerVersion(context.Background(), c); err != nil || major != 18 || minor != 5 {
			t.Errorf("GitLab back: got %d.%d, %v", major, minor, err)
		}
	}
	if got := f.calls.Load(); got != 3 {
		t.Errorf("asked %d times, want 3: two failures, one answer", got)
	}
}

// The example answer of GET /projects/:id/merge_requests/:iid/versions in
// GitLab's documentation (doc/api/merge_requests.md), with the patch ID
// of the second version made null.
const documentedVersions = `[{
  "id": 110,
  "head_commit_sha": "33e2ee8579fda5bc36accc9c6fbd0b4fefda9e30",
  "base_commit_sha": "eeb57dffe83deb686a60a71c16c32f71046868fd",
  "start_commit_sha": "eeb57dffe83deb686a60a71c16c32f71046868fd",
  "created_at": "2016-07-26T14:44:48.926Z",
  "merge_request_id": 105,
  "state": "collected",
  "real_size": "1",
  "patch_id_sha": "d504412d5b6e6739647e752aff8e468dde093f2f"
}, {
  "id": 108,
  "head_commit_sha": "3eed087b29835c48015768f839d76e5ea8f07a24",
  "base_commit_sha": "eeb57dffe83deb686a60a71c16c32f71046868fd",
  "start_commit_sha": "eeb57dffe83deb686a60a71c16c32f71046868fd",
  "created_at": "2016-07-25T14:21:33.028Z",
  "merge_request_id": 105,
  "state": "collected",
  "real_size": "1",
  "patch_id_sha": null
}]`

func TestVersionPatchID(t *testing.T) {
	var vs []Version
	if err := json.Unmarshal([]byte(documentedVersions), &vs); err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[0].PatchIDSHA != "d504412d5b6e6739647e752aff8e468dde093f2f" || vs[0].HeadCommitSHA != "33e2ee8579fda5bc36accc9c6fbd0b4fefda9e30" {
		t.Errorf("versions = %+v", vs)
	}
	if !vs[0].CreatedAt.Equal(time.Date(2016, 7, 26, 14, 44, 48, 926e6, time.UTC)) {
		t.Errorf("created_at = %v", vs[0].CreatedAt)
	}
	// A version GitLab computed no patch ID for (null): empty, unknown.
	if vs[1].PatchIDSHA != "" {
		t.Errorf("patch ID of a null = %q", vs[1].PatchIDSHA)
	}
}
