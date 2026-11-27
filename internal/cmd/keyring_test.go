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

// What someone who never logged in sees first: the host is not in glab's
// config, or there is no config at all. A plain `glab auth login` would
// put the token in the keyring of a current glab, and glcli would fail
// again: the message says how to log in so that glcli finds the token.
func TestUnknownHostSaysHowToLogIn(t *testing.T) {
	const advice = " (with --insecure-storage if glab is 1.111.0 or later: it then writes the token to its config file, in clear, instead of the keyring, which glcli does not read)\n"
	rt, _, stderr, path := fakeGitLabOnDisk(t, noRequest(t), keyringConfig)
	if code := run(rt, []string{"auth", "test", "--host", "new.example.com"}); code != 1 {
		t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
	}
	if got, want := stderr.String(), "glcli: no token for new.example.com in "+path+" (hosts there: code.pan.run, gitlab.com): run `glab auth login --hostname new.example.com`"+advice; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	// No config.yml at all.
	rt, _, stderr, _ = fakeGitLabOnDisk(t, noRequest(t), "")
	t.Setenv("GLAB_CONFIG_DIR", t.TempDir())
	if code := run(rt, []string{"auth", "test"}); code != 1 {
		t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
	}
	if got := stderr.String(); !strings.HasPrefix(got, "glcli: no token for gitlab.com in ") || !strings.HasSuffix(got, " (hosts there: ): run `glab auth login --hostname gitlab.com`"+advice) {
		t.Errorf("got %s", got)
	}
}

// glab's config comes with gitlab.com in it, with an empty token. Someone
// who only logged in to another instance, which is their default host,
// gets no hint about gitlab.com at each "auth status": its token is shown
// as missing, and that is all.
func TestAuthStatusDoesNotHintAtEveryHostWithoutAToken(t *testing.T) {
	rt, stdout, stderr, _ := fakeGitLabOnDisk(t, noRequest(t), `
host: code.pan.run
hosts:
    gitlab.com:
        token:
        api_protocol: https
    out.example.com:
        user: me
    code.pan.run:
        user: me
        token: glpat-SECRET
`)
	if code := run(rt, []string{"auth", "status"}); code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, stdout.String())
	}
	if _, ok := got["hints"]; ok {
		t.Errorf("no hint expected, got %s", got["hints"])
	}
	if out := stdout.String(); strings.Count(out, `"token":"missing"`) != 2 || strings.Contains(out, "keyring") {
		t.Errorf("want gitlab.com and out.example.com with a missing token and nothing else, got %s", out)
	}
}

// The default host without a token gets the hint, in the config or not:
// glcli uses it unless told otherwise.
func TestAuthStatusHintsAtTheDefaultHostWithoutAToken(t *testing.T) {
	for name, config := range map[string]string{
		"in the config, with an empty token": "hosts:\n    gitlab.com:\n        token:\n    code.pan.run:\n        token: glpat-SECRET\n",
		"not in the config":                  "hosts:\n    code.pan.run:\n        token: glpat-SECRET\n",
	} {
		rt, stdout, stderr, _ := fakeGitLabOnDisk(t, noRequest(t), config)
		if code := run(rt, []string{"auth", "status"}); code != 0 {
			t.Fatalf("%s: exit %d (stderr: %s)", name, code, stderr.String())
		}
		var got struct{ Hints []string }
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("%s: %v: %s", name, err, stdout.String())
		}
		if len(got.Hints) != 1 || !strings.HasPrefix(got.Hints[0], "no token for gitlab.com in ") || !strings.Contains(got.Hints[0], "`glab auth login --hostname gitlab.com` (with --insecure-storage if glab is 1.111.0 or later") {
			t.Errorf("%s: hints: %q", name, got.Hints)
		}
	}
}

// "keyring": true is for a token that is missing because of the keyring.
// A host with use_keyring and a token in the file, which glcli uses, is
// shown like any host with a token.
func TestAuthStatusKeyringOnlyWhenTheTokenIsMissing(t *testing.T) {
	rt, stdout, stderr, path := fakeGitLabOnDisk(t, noRequest(t), `
host: gitlab.com
hosts:
    gitlab.com:
        user: me
        use_keyring: "true"
        token: glpat-SECRET
    code.pan.run:
        user: me
        use_keyring: "true"
    file.example.com:
        use_keyring: "false"
        token: glpat-SECRET
`)
	if code := run(rt, []string{"auth", "status"}); code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
	}
	var got struct {
		Hosts []map[string]any
		Hints []string
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || len(got.Hosts) != 3 {
		t.Fatalf("%v: %s", err, stdout.String())
	}
	for _, h := range got.Hosts {
		_, keyring := h["keyring"]
		switch h["name"] {
		case "code.pan.run":
			if h["keyring"] != true || h["token"] != "missing" {
				t.Errorf("the token in the keyring: %v", h)
			}
		default:
			if keyring || h["token"] != "from "+path {
				t.Errorf("a token in the file: %v", h)
			}
		}
	}
	// One hint: for code.pan.run, whose token is in the keyring.
	if len(got.Hints) != 1 || !strings.HasPrefix(got.Hints[0], "no token for code.pan.run in ") || !strings.Contains(got.Hints[0], "set GLCLI_HOST=code.pan.run and GLCLI_TOKEN") {
		t.Errorf("hints: %q", got.Hints)
	}
	// With a token from the environment for the default host too.
	rt, stdout, stderr, _ = fakeGitLabOnDisk(t, noRequest(t), keyringConfig)
	t.Setenv("GLCLI_TOKEN", "glpat-env-SECRET")
	if code := run(rt, []string{"auth", "status"}); code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
	}
	if out := stdout.String(); strings.Contains(out, "keyring") || strings.Contains(out, "hints") || !strings.Contains(out, `"token":"from $GLCLI_TOKEN"`) {
		t.Errorf("gitlab.com has a token, from the environment: got %s", out)
	}
}
