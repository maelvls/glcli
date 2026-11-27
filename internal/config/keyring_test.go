package config

import (
	"strings"
	"testing"
)

// Written by hand: no glab was run. Since glab 1.111.0, `glab auth login`
// keeps the token in the keyring of the operating system when there is
// one, and the host is left in config.yml with use_keyring and no token
// (https://gitlab.com/gitlab-org/cli/-/blob/9858866c61b13bfdcb0551b407fa558c4c868d8a/internal/commands/auth/login/login.go:
// `cfg.Set(hostname, "use_keyring", keyringPref)`, and
// internal/config/config.go, syncKeyring: "store in the keyring and drop
// the plaintext copy from the config file by returning an empty value").
// Only "token", "job_token", "oauth2_refresh_token" and
// "client_cert_password" go to the keyring (internal/config/schema.go,
// Keyring: true): is_oauth2 and oauth2_expiry_date stay in the file.
// glab's own tests check for `use_keyring: "true"` in what it writes
// (internal/config/config_test.go).
const keyringSample = `
host: gitlab.com
hosts:
    gitlab.com:
        api_protocol: https
        user: me
        use_keyring: "true"
        is_oauth2: "true"
        oauth2_expiry_date: "2026-11-27T19:42:10+01:00"
    code.pan.run:
        user: other
        use_keyring: "true"
    out.example.com:
        api_protocol: https
        user: nobody
    file.example.com:
        use_keyring: "false"
        token: glpat-SECRET-file
    both.example.com:
        use_keyring: "true"
        token: glpat-SECRET-both
`

func TestNoTokenBecauseOfTheKeyring(t *testing.T) {
	clearEnv(t)
	c, err := Parse([]byte(keyringSample), "/x/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]string{
		// The default host: GLCLI_TOKEN is enough.
		"gitlab.com": "no token for gitlab.com in /x/config.yml: glab keeps it in the keyring of the operating system (use_keyring is true for this host), which glcli does not read. " +
			"Either run `glab auth login --hostname gitlab.com --insecure-storage` (glab 1.111.0 or later), which makes glab write the token to that file, in clear, " +
			"or set GLCLI_TOKEN to a personal access token",
		// Another host: GLCLI_TOKEN is only used for the default one.
		"code.pan.run": "no token for code.pan.run in /x/config.yml: glab keeps it in the keyring of the operating system (use_keyring is true for this host), which glcli does not read. " +
			"Either run `glab auth login --hostname code.pan.run --insecure-storage` (glab 1.111.0 or later), which makes glab write the token to that file, in clear, " +
			"or set GLCLI_HOST=code.pan.run and GLCLI_TOKEN to a personal access token (GLCLI_TOKEN is only used for the default host, which is gitlab.com)",
		// No token and no use_keyring: logged out, or an older glab.
		"out.example.com": "no token for out.example.com in /x/config.yml: the host is there without a token: glab is logged out of it, or keeps its token in the keyring of the operating system, which glcli does not read. " +
			"Either run `glab auth login --hostname out.example.com`, with --insecure-storage if glab is 1.111.0 or later (it then writes the token to that file, in clear, instead of the keyring), " +
			"or set GLCLI_HOST=out.example.com and GLCLI_TOKEN to a personal access token (GLCLI_TOKEN is only used for the default host, which is gitlab.com)",
		// A host glab's config does not have: as before.
		"new.example.com": "no token for new.example.com in /x/config.yml (hosts there: both.example.com, code.pan.run, file.example.com, gitlab.com, out.example.com): run `glab auth login --hostname new.example.com`",
	} {
		h, err := c.Host(host)
		if err == nil || err.Error() != want {
			t.Errorf("%s:\n got %v\nwant %s", host, err, want)
		}
		if h.Token != "" || h.Name != host {
			t.Errorf("%s: %+v", host, h)
		}
	}
	if h := c.Hosts["gitlab.com"]; !h.Keyring || !h.OAuth || h.Expiry.IsZero() {
		t.Errorf("gitlab.com: %+v", h)
	}
	// A token in the file is used, whatever use_keyring says.
	for host, want := range map[string]string{"file.example.com": "glpat-SECRET-file", "both.example.com": "glpat-SECRET-both"} {
		if h, err := c.Host(host); err != nil || h.Token != want || h.Keyring != (host == "both.example.com") {
			t.Errorf("%s: %+v %v", host, h, err)
		}
	}
}

// With GLCLI_HOST, the host it names is the default one, and GLCLI_TOKEN is
// its token: the way out the error gives works.
func TestEnvTokenForAKeyringHost(t *testing.T) {
	c, err := Parse([]byte(keyringSample), "/x/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"GLCLI_TOKEN", "GITLAB_TOKEN"} {
		clearEnv(t)
		t.Setenv(env, "glpat-env")
		h, err := c.Host("gitlab.com")
		if err != nil || h.Token != "glpat-env" || h.TokenSource != "$"+env || h.OAuth || !h.Expiry.IsZero() {
			t.Errorf("$%s: gitlab.com: %+v %v", env, h, err)
		}
		// Not the default host: the token of the environment is not for it.
		if _, err := c.Host("code.pan.run"); err == nil || !strings.Contains(err.Error(), "set GLCLI_HOST=code.pan.run and GLCLI_TOKEN") {
			t.Errorf("$%s: code.pan.run: %v", env, err)
		}
		t.Setenv("GLCLI_HOST", "code.pan.run")
		h, err = c.Host("code.pan.run")
		if err != nil || h.Token != "glpat-env" || h.TokenSource != "$"+env {
			t.Errorf("$%s and $GLCLI_HOST: code.pan.run: %+v %v", env, h, err)
		}
		// And gitlab.com is now told about GLCLI_HOST.
		if _, err := c.Host("gitlab.com"); err == nil || !strings.Contains(err.Error(), "set GLCLI_HOST=gitlab.com and GLCLI_TOKEN to a personal access token (GLCLI_TOKEN is only used for the default host, which is code.pan.run)") {
			t.Errorf("$%s and $GLCLI_HOST: gitlab.com: %v", env, err)
		}
	}
}

// use_keyring is read as glab reads it: its text, compared with "true"
// (https://gitlab.com/gitlab-org/cli/-/blob/9858866c61b13bfdcb0551b407fa558c4c868d8a/internal/config/host_config.go,
// HostUsesKeyring). Nothing written there is an error.
func TestParseUseKeyring(t *testing.T) {
	clearEnv(t)
	for value, want := range map[string]bool{
		`"true"`:              true,
		`true`:                true,
		`!!null "true"`:       true,
		`"false"`:             false,
		`false`:               false,
		``:                    false,
		`~`:                   false,
		`yes`:                 false,
		`[true]`:              false,
		`{true: true}`:        false,
		`!!null ` + fakeToken: false,
	} {
		data := "hosts:\n    gitlab.com:\n        use_keyring: " + value + "\n        user: me\n"
		c, err := Parse([]byte(data), "/x/config.yml")
		if err != nil {
			t.Errorf("use_keyring: %s: %v", value, err)
			checkError(t, []byte(data), err)
			continue
		}
		h, err := c.Host("gitlab.com")
		if h.Keyring != want || err == nil || strings.Contains(err.Error(), "use_keyring is true") != want {
			t.Errorf("use_keyring: %s: Keyring=%v, want %v (%v)", value, h.Keyring, want, err)
		}
		if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "glpat") {
			t.Errorf("use_keyring: %s: the error repeats the file: %v", value, err)
		}
	}
}
