package gitlab

import (
	"context"
	"testing"
)

// An answer to the version request that asking again will not change is
// remembered for as long as the process lives: a wait would ask at every
// poll otherwise.
func TestPatchIDsTrustedRemembersARefusal(t *testing.T) {
	tests := []struct {
		name   string
		status int64
		answer string
	}{
		{name: "403", status: 403},
		{name: "404", status: 404},
		{name: "401", status: 401},
		{name: "not JSON", answer: "<html><body>Sign in</body></html>"},
		{name: "a version that cannot be read", answer: `{"version": "main"}`},
	}
	for _, tt := range tests {
		f := &releaseServer{answer: tt.answer}
		if tt.status != 0 {
			f.answer = `{"version": "18.5.1-ee"}`
		}
		f.status.Store(tt.status)
		c := f.client(t)
		for range 3 {
			if PatchIDsTrusted(context.Background(), c, VersionTimeout) {
				t.Errorf("%s: trusted", tt.name)
			}
		}
		// Even when GitLab would now answer.
		f.status.Store(0)
		if PatchIDsTrusted(context.Background(), c, VersionTimeout) && tt.status != 0 {
			t.Errorf("%s, then an answer: trusted, want the refusal remembered", tt.name)
		}
		if got := f.calls.Load(); got != 1 {
			t.Errorf("%s: version asked %d times, want 1", tt.name, got)
		}
	}
}

// A failure that may pass is not remembered: the next call asks again.
func TestPatchIDsTrustedAsksAgainAfterAFailureThatMayPass(t *testing.T) {
	for _, status := range []int64{500, 502, 503, 429} {
		f := &releaseServer{answer: `{"version": "18.5.1-ee"}`}
		f.status.Store(status)
		c := f.client(t)
		for range 2 {
			if PatchIDsTrusted(context.Background(), c, VersionTimeout) {
				t.Errorf("%d: trusted", status)
			}
		}
		f.status.Store(0)
		if !PatchIDsTrusted(context.Background(), c, VersionTimeout) {
			t.Errorf("%d, then an answer: not trusted", status)
		}
		if got := f.calls.Load(); got != 3 {
			t.Errorf("%d: version asked %d times, want 3", status, got)
		}
	}
}

// The caller giving up says nothing of GitLab: nothing is remembered.
func TestPatchIDsTrustedDoesNotRememberAnInterruption(t *testing.T) {
	f := &releaseServer{answer: `{"version": "18.5.1-ee"}`}
	c := f.client(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if PatchIDsTrusted(ctx, c, VersionTimeout) {
		t.Error("interrupted: trusted")
	}
	if !PatchIDsTrusted(context.Background(), c, VersionTimeout) {
		t.Error("asked again: not trusted")
	}
}
