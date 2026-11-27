package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// After a 401, the hint names the host to log in to, as glab knows it
// (the key under "hosts:"), not the API host.
func TestRejectedTokenHintNamesTheHost(t *testing.T) {
	rt, stdout, stderr := fakeGitLab(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"401 Unauthorized"}`)
	})
	if code := run(rt, []string{"auth", "test", "--host", "code.pan.run"}); code != 1 {
		t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
	}
	got := stderr.String()
	if !strings.Contains(got, "`glab auth login --hostname code.pan.run`") {
		t.Errorf("the hint must name the host, got:\n%s", got)
	}
	if strings.Contains(got, "<host>") || strings.Contains(got, "api.code.pan.run") {
		t.Errorf("the hint must name the glab host, got:\n%s", got)
	}
	if !strings.Contains(got, "glcli: GitLab GET user: HTTP 401: 401 Unauthorized\n") {
		t.Errorf("the error itself must not change, got:\n%s", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("nothing expected on stdout, got %s", stdout.String())
	}
}

func TestRejectedTokenHintWithoutHost(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &gitlab.APIError{Method: "GET", Path: "user", Status: 401})
	if got := rejectedTokenHint(err); !strings.Contains(got, "`glab auth login --hostname <host>`") {
		t.Errorf("without a host, the hint keeps the placeholder, got %q", got)
	}
	err = fmt.Errorf("wrapped: %w", &gitlab.APIError{Host: "gitlab.com", Method: "GET", Path: "user", Status: 401})
	if got := rejectedTokenHint(err); !strings.Contains(got, "`glab auth login --hostname gitlab.com`") {
		t.Errorf("want the host of the error, got %q", got)
	}
	if got := rejectedTokenHint(errors.New("something else")); !strings.Contains(got, "--hostname <host>") {
		t.Errorf("got %q", got)
	}
}
