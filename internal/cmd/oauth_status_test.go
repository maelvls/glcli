package cmd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func noRequest(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request expected, got %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

// "auth status" says which hosts glab logged in to with OAuth and when
// their token expires, and never prints a token.
func TestAuthStatusShowsOAuthHosts(t *testing.T) {
	rt, stdout, stderr, path := fakeGitLabOnDisk(t, noRequest(t), oauthConfig("oauth-SECRET", "2026-11-27T19:42:10+01:00"))
	// Before the token expires: see TestAuthStatusHintsAtAnExpiredOAuthToken.
	rt.Now = func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) }
	if code := run(rt, []string{"auth", "status"}); code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
	}
	var got struct {
		Config string
		Hosts  []map[string]any
		Hints  []string
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, stdout.String())
	}
	if got.Config != path || len(got.Hosts) != 2 || len(got.Hints) != 0 {
		t.Fatalf("got %s", stdout.String())
	}
	pat, oauth := got.Hosts[0], got.Hosts[1]
	if oauth["name"] != "gitlab.com" || oauth["token_kind"] != "oauth" || oauth["expires_at"] != "2026-11-27T19:42:10+01:00" || oauth["token"] != "from "+path || oauth["default"] != true {
		t.Errorf("the OAuth host: %v", oauth)
	}
	if pat["name"] != "code.pan.run" || pat["token"] != "from "+path {
		t.Errorf("the host with a personal access token: %v", pat)
	}
	for _, key := range []string{"token_kind", "expires_at"} {
		if _, ok := pat[key]; ok {
			t.Errorf("a personal access token has no %s: %v", key, pat)
		}
	}
	if out := stdout.String() + stderr.String(); strings.Contains(out, "SECRET") {
		t.Errorf("a token was printed: %s", out)
	}
}

// Without an OAuth host, "auth status" prints what it always printed.
func TestAuthStatusUnchangedWithoutOAuth(t *testing.T) {
	rt, stdout, stderr, path := fakeGitLabOnDisk(t, noRequest(t), `
host: gitlab.com
hosts:
    gitlab.com:
        api_host: gitlab.com
        api_protocol: https
        user: me
        token: glpat-SECRET
    code.pan.run:
        token: glpat-SECRET
        user: other
`)
	if code := run(rt, []string{"auth", "status"}); code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
	}
	p, _ := json.Marshal(path)
	from, _ := json.Marshal("from " + path)
	want := `{"config":` + string(p) + `,"hosts":[` +
		`{"name":"code.pan.run","user":"other","token":` + string(from) + `,"api_url":"https://code.pan.run/api/v4"},` +
		`{"name":"gitlab.com","user":"me","token":` + string(from) + `,"default":true,"api_url":"https://gitlab.com/api/v4"}]}` + "\n"
	if stdout.String() != want || stderr.Len() != 0 {
		t.Errorf("got %s%s\nwant %s", stdout.String(), stderr.String(), want)
	}
}

// A token from the environment is not an OAuth one, whatever glab's config
// says of the host it is used for.
func TestAuthStatusEnvTokenIsNotOAuth(t *testing.T) {
	rt, stdout, stderr, _ := fakeGitLabOnDisk(t, noRequest(t), oauthConfig("oauth-SECRET", "2026-11-27T19:42:10+01:00"))
	t.Setenv("GLCLI_TOKEN", "glpat-env-SECRET")
	if code := run(rt, []string{"auth", "status"}); code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `"token":"from $GLCLI_TOKEN"`) || strings.Contains(out, "oauth") || strings.Contains(out, "expires_at") || strings.Contains(out, "SECRET") {
		t.Errorf("got %s", out)
	}
}
