package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/config"
)

// Patch IDs are trusted from GitLab 18.2 on, and any doubt is "no".
func TestPatchIDsTrusted(t *testing.T) {
	tests := []struct {
		answer string
		want   bool
	}{
		{`{"version": "18.2.0"}`, true},
		{`{"version": "18.2.0-pre"}`, true},
		{`{"version": "18.5.1-ee"}`, true},
		{`{"version": "18.10.0"}`, true},
		{`{"version": "19.0.2"}`, true},
		{`{"version": "20.1.0"}`, true},
		{`{"version": "18.1.6-ee"}`, false},
		{`{"version": "18.0.0"}`, false},
		{`{"version": "17.11.4"}`, false},
		{`{"version": "17.2.0"}`, false},
		{`{"version": "2.18.0"}`, false},
		{`{"version": "main"}`, false},
		{`{"version": ""}`, false},
		{`{}`, false},
	}
	for _, tt := range tests {
		f := &releaseServer{answer: tt.answer}
		c := f.client(t)
		for range 2 {
			if got := PatchIDsTrusted(context.Background(), c, VersionTimeout); got != tt.want {
				t.Errorf("%s: trusted = %v, want %v", tt.answer, got, tt.want)
			}
		}
		if got := f.calls.Load(); got != 1 {
			t.Errorf("%s: version asked %d times, want 1", tt.answer, got)
		}
	}
}

// A GitLab that fails to say its version is not trusted, and is asked
// again the next time.
func TestPatchIDsTrustedAsksAgainAfterAFailure(t *testing.T) {
	f := &releaseServer{answer: `{"version": "18.5.1-ee"}`}
	c := f.client(t)
	f.status.Store(500)
	if PatchIDsTrusted(context.Background(), c, VersionTimeout) {
		t.Error("GitLab failing: trusted")
	}
	f.status.Store(0)
	for range 2 {
		if !PatchIDsTrusted(context.Background(), c, VersionTimeout) {
			t.Error("GitLab back: not trusted")
		}
	}
	if got := f.calls.Load(); got != 2 {
		t.Errorf("version asked %d times, want 2: one failure, one answer", got)
	}
}

// A GitLab that does not answer is given the bound, not what is left of
// the caller's context, and is asked again the next time.
func TestPatchIDsTrustedIsBounded(t *testing.T) {
	var hang atomic.Bool
	hang.Store(true)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if hang.Load() {
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(`{"version": "18.5.1-ee"}`))
	}))
	t.Cleanup(srv.Close)
	// A host no test has asked: a version once given is remembered.
	c := New(config.Host{Name: fmt.Sprintf("release-%d", releaseHosts.Add(1)), Token: "x"})
	c.BaseURL = srv.URL

	start := time.Now()
	if PatchIDsTrusted(context.Background(), c, 50*time.Millisecond) {
		t.Error("GitLab not answering: trusted")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("waited %s for the version, want about 50ms", took)
	}
	hang.Store(false)
	if !PatchIDsTrusted(context.Background(), c, VersionTimeout) {
		t.Error("GitLab back: not trusted")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("version asked %d times, want 2", got)
	}
}
