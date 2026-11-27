package cmd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A glab config, written by hand, of a glab that keeps its tokens in the
// keyring of the operating system (its default since 1.111.0): the hosts
// are there without a token. See internal/config/keyring_test.go.
const keyringConfig = `
host: gitlab.com
hosts:
    gitlab.com:
        api_protocol: https
        user: me
        use_keyring: "true"
        is_oauth2: "true"
        oauth2_expiry_date: "2020-01-02T03:04:05Z"
    code.pan.run:
        user: me
        token: glpat-SECRET
`

// A command on a host whose token is in the keyring says so, and how to
// give glcli a token, instead of telling to log in again with glab, which
// would put the token in the keyring again.
func TestKeyringHostSaysWhatToDo(t *testing.T) {
	rt, stdout, stderr, path := fakeGitLabOnDisk(t, noRequest(t), keyringConfig)
	if code := run(rt, []string{"auth", "test"}); code != 1 {
		t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
	}
	want := "glcli: no token for gitlab.com in " + path + ": glab keeps it in the keyring of the operating system (use_keyring is true for this host), which glcli does not read. " +
		"Either run `glab auth login --hostname gitlab.com --insecure-storage` (glab 1.111.0 or later), which makes glab write the token to that file, in clear, " +
		"or set GLCLI_TOKEN to a personal access token\n"
	if got := stderr.String(); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if stdout.Len() != 0 {
		t.Errorf("nothing expected on stdout, got %s", stdout.String())
	}
}

// "auth status" shows such a host with its token missing, and why.
func TestAuthStatusShowsKeyringHosts(t *testing.T) {
	rt, stdout, stderr, _ := fakeGitLabOnDisk(t, noRequest(t), keyringConfig)
	rt.Now = func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) }
	if code := run(rt, []string{"auth", "status"}); code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
	}
	var got struct {
		Hosts []map[string]any
		Hints []string
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || len(got.Hosts) != 2 {
		t.Fatalf("%v: %s", err, stdout.String())
	}
	pat, keyring := got.Hosts[0], got.Hosts[1]
	if keyring["name"] != "gitlab.com" || keyring["token"] != "missing" || keyring["keyring"] != true || keyring["token_kind"] != "oauth" {
		t.Errorf("the host with its token in the keyring: %v", keyring)
	}
	if _, ok := pat["keyring"]; ok || pat["name"] != "code.pan.run" || !strings.HasPrefix(pat["token"].(string), "from ") {
		t.Errorf("the host with its token in the file: %v", pat)
	}
	// One hint, for the missing token: there is no token to say expired.
	if len(got.Hints) != 1 || !strings.HasPrefix(got.Hints[0], "no token for gitlab.com in ") || !strings.Contains(got.Hints[0], "--insecure-storage") || !strings.HasSuffix(got.Hints[0], "or set GLCLI_TOKEN to a personal access token.") {
		t.Errorf("hints: %q", got.Hints)
	}
	if strings.Contains(stdout.String(), "SECRET") {
		t.Errorf("a token was printed: %s", stdout.String())
	}
}

// The way out the error gives: with GLCLI_TOKEN, the host is used with
// that token, sent as an access token.
func TestKeyringHostWithEnvToken(t *testing.T) {
	g := &gate{accept: "PRIVATE-TOKEN: glpat-env-SECRET"}
	rt, stdout, stderr, _ := fakeGitLabOnDisk(t, g.handler(currentUser), keyringConfig)
	t.Setenv("GLCLI_TOKEN", "glpat-env-SECRET")
	if code := run(rt, []string{"auth", "test"}); code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
	}
	if got := stdout.String(); got != `{"host":"gitlab.com","username":"me","name":"Me"}`+"\n" {
		t.Errorf("got %s", got)
	}
}
