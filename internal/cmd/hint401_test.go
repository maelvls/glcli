package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// After a 401, the hint names the host to log in to, as glab knows it
// (the key under "hosts:"), not the API host.
func TestRejectedTokenHintNamesTheHost(t *testing.T) {
	rt, stdout, stderr := fakeGitLab(t, unauthorized)
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

func unauthorized(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprint(w, `{"message":"401 Unauthorized"}`)
}

// $GLCLI_TOKEN and $GITLAB_TOKEN replace the token of the default host:
// when that token is rejected, logging in with glab would change nothing.
func TestRejectedTokenHintNamesTheVariable(t *testing.T) {
	for _, env := range []string{"GLCLI_TOKEN", "GITLAB_TOKEN"} {
		rt, _, stderr := fakeGitLab(t, unauthorized)
		t.Setenv(env, "bad")
		if code := run(rt, []string{"auth", "test"}); code != 1 {
			t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
		}
		got := stderr.String()
		if !strings.Contains(got, "the token from $"+env+" was rejected by gitlab.com: unset it") {
			t.Errorf("the hint must name $%s, got:\n%s", env, got)
		}
		if strings.Contains(got, "glab auth login") {
			t.Errorf("$%s: logging in with glab would change nothing, got:\n%s", env, got)
		}

		// Another host than the default one keeps the token of glab's config.
		rt, _, stderr = fakeGitLab(t, unauthorized)
		t.Setenv(env, "bad")
		if code := run(rt, []string{"auth", "test", "--host", "code.pan.run"}); code != 1 {
			t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
		}
		if got := stderr.String(); !strings.Contains(got, "`glab auth login --hostname code.pan.run`") || strings.Contains(got, "$"+env) {
			t.Errorf("$%s is not used for code.pan.run, got:\n%s", env, got)
		}
	}
}

// A redirect to another host drops the token: a 401 there does not mean
// that the token was rejected.
func TestNoRejectedTokenHintAfterRedirectToAnotherHost(t *testing.T) {
	var sent string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = r.Header.Get("PRIVATE-TOKEN")
		unauthorized(w, r)
	}))
	defer other.Close()
	rt, _, stderr := fakeGitLab(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/users/sign_in", http.StatusFound)
	})
	if code := run(rt, []string{"auth", "test"}); code != 1 {
		t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
	}
	got := stderr.String()
	if sent != "" {
		t.Fatalf("the token was sent to another host: %q", sent)
	}
	if strings.Contains(got, "glab auth login") || strings.Contains(got, "token was rejected:") {
		t.Errorf("the token was not sent, so it was not rejected, got:\n%s", got)
	}
	if !strings.Contains(got, "redirected to "+strings.TrimPrefix(other.URL, "http://")+", where the token is not sent") {
		t.Errorf("the hint must tell about the redirect, got:\n%s", got)
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
	err = &gitlab.APIError{Host: "gitlab.com", TokenSource: "/home/me/.config/glab-cli/config.yml", Status: 401}
	if got := rejectedTokenHint(err); !strings.Contains(got, "`glab auth login --hostname gitlab.com`") {
		t.Errorf("a token from glab's config: want glab auth login, got %q", got)
	}
}
