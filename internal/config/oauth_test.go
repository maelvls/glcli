package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Written by hand: no glab was run. The keys of the OAuth host and their
// values are the ones glab's source writes after an OAuth login
// (https://gitlab.com/gitlab-org/cli/-/blob/9858866c61b13bfdcb0551b407fa558c4c868d8a/internal/oauth2/config.go,
// func marshal):
//
//	err := cfg.Set(hostname, "is_oauth2", "true")
//	err = cfg.Set(hostname, "oauth2_refresh_token", token.RefreshToken)
//	err = cfg.Set(hostname, "oauth2_expiry_date", token.Expiry.Format(time.RFC3339))
//	err = cfg.Set(hostname, "token", token.AccessToken)
//
// Set stores each value in a "!!str" node (internal/config/config.go,
// SetStringValue), which the yaml package writes in double quotes when the
// text would otherwise read as something else: "true", and a date. (Seen by
// encoding such nodes with gopkg.in/yaml.v3; glab uses a fork of it,
// go.yaml.in/yaml/v3.)
const oauthRefreshToken = "glort-REFRESH0123456789"

const oauthSample = `
host: gitlab.com
hosts:
    gitlab.com:
        api_host: gitlab.com
        api_protocol: https
        git_protocol: ssh
        user: me
        is_oauth2: "true"
        oauth2_refresh_token: ` + oauthRefreshToken + `
        oauth2_expiry_date: "2026-11-27T19:42:10+01:00"
        token: glsecret-OAUTH0123456789
    code.pan.run:
        token: glpat-bbb
        user: other
    nothing.example.com:
        api_protocol: https
        user: nobody
`

func TestParseOAuthHost(t *testing.T) {
	clearEnv(t)
	c, err := Parse([]byte(oauthSample), "/x/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.Host("gitlab.com")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 27, 18, 42, 10, 0, time.UTC)
	if !h.OAuth || !h.Expiry.Equal(want) || h.Token != "glsecret-OAUTH0123456789" || h.User != "me" || h.TokenSource != "/x/config.yml" {
		t.Errorf("the OAuth host: %+v", h)
	}
	// The offset glab wrote is kept, for messages in the user's own time.
	if got := h.Expiry.Format(time.RFC3339); got != "2026-11-27T19:42:10+01:00" {
		t.Errorf("expiry: %s", got)
	}
	h, err = c.Host("code.pan.run")
	if err != nil || h.OAuth || !h.Expiry.IsZero() || h.Token != "glpat-bbb" {
		t.Errorf("the host with a personal access token: %+v %v", h, err)
	}
	if h, err := c.Host("nothing.example.com"); err == nil || h.Token != "" || h.OAuth {
		t.Errorf("the host without a token: %+v %v", h, err)
	}
	// The refresh token is in no field of the config.
	if all := fmt.Sprintf("%+v", c); strings.Contains(all, oauthRefreshToken) || strings.Contains(all, "REFRESH") {
		t.Errorf("the refresh token was kept: %s", all)
	}
}

// A host is an OAuth one exactly when glab would say so: it compares the
// text of is_oauth2 with "true"
// (https://gitlab.com/gitlab-org/cli/-/blob/9858866c61b13bfdcb0551b407fa558c4c868d8a/internal/api/client.go:
// isOAuth2 := isOAuth2Cfg == "true"). Nothing written there is an error.
func TestParseIsOAuth2(t *testing.T) {
	clearEnv(t)
	for value, want := range map[string]bool{
		`"true"`:              true,
		`true`:                true,
		`'true'`:              true,
		`!!str true`:          true,
		`!!null true`:         true,
		`!!null "true"`:       true,
		`"false"`:             false,
		`false`:               false,
		``:                    false,
		`~`:                   false,
		`!!null`:              false,
		`""`:                  false,
		`True`:                false,
		`yes`:                 false,
		`1`:                   false,
		`[true]`:              false,
		`{true: true}`:        false,
		`!!null ` + fakeToken: false,
	} {
		data := "hosts:\n    gitlab.com:\n        is_oauth2: " + value + "\n        token: " + fakeToken + "\n        oauth2_expiry_date: \"2026-11-27T19:42:10+01:00\"\n"
		c, err := Parse([]byte(data), "/x/config.yml")
		if err != nil {
			t.Errorf("is_oauth2: %s: %v", value, err)
			continue
		}
		h, err := c.Host("gitlab.com")
		if err != nil || h.OAuth != want || h.Token != fakeToken {
			t.Errorf("is_oauth2: %s: OAuth=%v, want %v (%v)", value, h.OAuth, want, err)
		}
		// The expiry is the one of an OAuth token: it means nothing else.
		if h.Expiry.IsZero() == want {
			t.Errorf("is_oauth2: %s: expiry %v", value, h.Expiry)
		}
	}
}

// The date is read in the layouts glab reads
// (https://gitlab.com/gitlab-org/cli/-/blob/9858866c61b13bfdcb0551b407fa558c4c868d8a/internal/oauth2/config.go,
// ParseExpiryDate: time.RFC3339, time.RFC822, time.RFC822Z). A date that
// cannot be read is no date, never an error.
func TestParseOAuthExpiry(t *testing.T) {
	clearEnv(t)
	want := time.Date(2026, 11, 27, 18, 42, 0, 0, time.UTC)
	for value, want := range map[string]time.Time{
		`"2026-11-27T19:42:00+01:00"`:       want,
		`2026-11-27T19:42:00+01:00`:         want,
		`"2026-11-27T18:42:00Z"`:            want,
		`2026-11-27T18:42:00Z`:              want,
		`!!null 2026-11-27T18:42:00Z`:       want,
		`!!null "2026-11-27T18:42:00Z"`:     want,
		`!!timestamp 2026-11-27T18:42:00Z`:  want,
		`27 Nov 26 19:42 +0100`:             want,
		`"27 Nov 26 19:42 +0100"`:           want,
		`27 Nov 26 18:42 UTC`:               want,
		`27 Nov 26 18:42 XYZ`:               {}, // a zone Go knows no offset for
		``:                                  {},
		`~`:                                 {},
		`""`:                                {},
		`not-a-date`:                        {},
		`2026-11-27`:                        {},
		`1795804920`:                        {},
		`[2026-11-27T18:42:00Z]`:            {},
		`{at: 2026-11-27T18:42:00Z}`:        {},
		`!!null ` + fakeToken:               {},
		`"` + fakeToken + `"`:               {},
		`2026-11-27T18:42:00Z ` + fakeToken: {},
	} {
		data := "hosts:\n    gitlab.com:\n        is_oauth2: \"true\"\n        token: " + fakeToken + "\n        oauth2_expiry_date: " + value + "\n"
		c, err := Parse([]byte(data), "/x/config.yml")
		if err != nil {
			t.Errorf("oauth2_expiry_date: %s: %v", value, err)
			continue
		}
		h, err := c.Host("gitlab.com")
		if err != nil || !h.OAuth || h.Token != fakeToken || !h.Expiry.Equal(want) {
			t.Errorf("oauth2_expiry_date: %s: got %v, want %v (%+v, %v)", value, h.Expiry, want, h.OAuth, err)
		}
	}
}

// glab can leave the !!null tag of a once empty value on what it writes
// there (see TestParseTokenTaggedNull): an OAuth host is read all the same.
func TestParseOAuthHostTaggedNull(t *testing.T) {
	clearEnv(t)
	c, err := Parse([]byte(`
hosts:
    gitlab.com:
        user: !!null me
        is_oauth2: !!null "true"
        oauth2_refresh_token: !!null `+oauthRefreshToken+`
        oauth2_expiry_date: !!null "2026-11-27T19:42:10+01:00"
        token: !!null glsecret-OAUTH0123456789
    code.pan.run:
        is_oauth2: !!null
        oauth2_expiry_date: !!null
        token: !!null glpat-bbb
`), "/x/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.Host("gitlab.com")
	if err != nil || !h.OAuth || h.Token != "glsecret-OAUTH0123456789" || !h.Expiry.Equal(time.Date(2026, 11, 27, 18, 42, 10, 0, time.UTC)) {
		t.Errorf("gitlab.com: %+v %v", h, err)
	}
	h, err = c.Host("code.pan.run")
	if err != nil || h.OAuth || h.Token != "glpat-bbb" || !h.Expiry.IsZero() {
		t.Errorf("code.pan.run: %+v %v", h, err)
	}
}

// $GLCLI_TOKEN and $GITLAB_TOKEN replace the token of the default host:
// what glab's config says of the token they replace (OAuth, its expiry)
// does not hold for them.
func TestEnvTokenIsNotOAuth(t *testing.T) {
	for _, env := range []string{"GLCLI_TOKEN", "GITLAB_TOKEN"} {
		clearEnv(t)
		t.Setenv(env, "glpat-env")
		c, err := Parse([]byte(oauthSample), "/x/config.yml")
		if err != nil {
			t.Fatal(err)
		}
		h, err := c.Host("gitlab.com")
		if err != nil || h.OAuth || !h.Expiry.IsZero() || h.Token != "glpat-env" || h.TokenSource != "$"+env {
			t.Errorf("$%s: the default host: %+v %v", env, h, err)
		}
		// Another host than the default one keeps what the config says.
		t.Setenv("GLCLI_HOST", "code.pan.run")
		h, err = c.Host("gitlab.com")
		if err != nil || !h.OAuth || h.Expiry.IsZero() || h.Token != "glsecret-OAUTH0123456789" || h.TokenSource != "/x/config.yml" {
			t.Errorf("$%s: gitlab.com, when it is not the default host: %+v %v", env, h, err)
		}
	}
}

// What is wrong in a config with an OAuth host is said as for any other:
// with the shape checkError allows, and nothing of the file, where the
// access token and the refresh token are.
func TestParseOAuthErrors(t *testing.T) {
	clearEnv(t)
	for _, tc := range oauthBadConfigs {
		c, err := Parse([]byte(tc.data), "/x/config.yml")
		if err == nil {
			t.Errorf("%s: expected an error, got %+v", tc.name, c)
			continue
		}
		if got, want := err.Error(), "reading /x/config.yml: "+tc.want; got != want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, want)
		}
		checkError(t, []byte(tc.data), err)
		if strings.Contains(err.Error(), "REFRESH") || strings.Contains(err.Error(), "glort") {
			t.Errorf("%s: the error leaks the refresh token: %v", tc.name, err)
		}
	}
}

const oauthHost = "hosts:\n    gitlab.com:\n        is_oauth2: \"true\"\n"

var oauthBadConfigs = []struct{ name, data, want string }{
	{"unclosed quote in the refresh token", oauthHost + "        oauth2_refresh_token: \"" + oauthRefreshToken + "\n        token: " + fakeToken + "\n", "invalid YAML near line 4: found unexpected end of stream"},
	{"unclosed quote in the expiry", oauthHost + "        token: " + fakeToken + "\n        oauth2_expiry_date: \"2026-11-27T19:42:10+01:00\n", "invalid YAML near line 5: found unexpected end of stream"},
	{"bad indentation after the refresh token", oauthHost + "        oauth2_refresh_token: " + oauthRefreshToken + "\n      token: " + fakeToken + "\n", "invalid YAML near line 1: did not find expected key"},
	{"unknown anchor as refresh token", oauthHost + "        oauth2_refresh_token: *" + oauthRefreshToken + "\n", "invalid YAML"},
	{"duplicate refresh token", oauthHost + "        oauth2_refresh_token: " + oauthRefreshToken + "\n        oauth2_refresh_token: " + oauthRefreshToken + "\n", "line 5: duplicate key (first at line 4)"},
	{"duplicate is_oauth2", oauthHost + "        is_oauth2: " + fakeToken + "\n", "line 4: duplicate key (first at line 3)"},
	{"list as token of an OAuth host", oauthHost + "        oauth2_refresh_token: " + oauthRefreshToken + "\n        token: [" + fakeToken + "]\n", "line 5: token is not a string"},
}

// The keys glcli does not need are not looked at: whatever is there, even
// a list where glab writes a text, the file is read as before.
func TestParseOAuthKeysOfAnyShape(t *testing.T) {
	clearEnv(t)
	for _, extra := range []string{
		"oauth2_refresh_token: [" + oauthRefreshToken + "]",
		"oauth2_refresh_token: {" + oauthRefreshToken + ": " + fakeToken + "}",
		"oauth2_refresh_token: !!null " + oauthRefreshToken,
		"is_oauth2: [" + fakeToken + "]",
		"oauth2_expiry_date: [" + fakeToken + "]",
		"oauth2_expiry_date: {" + fakeToken + ": 1}",
	} {
		data := "hosts:\n    gitlab.com:\n        token: glpat-aaa\n        " + extra + "\n"
		c, err := Parse([]byte(data), "/x/config.yml")
		if err != nil {
			t.Errorf("%s: %v", extra, err)
			checkError(t, []byte(data), err)
			continue
		}
		if h, err := c.Host("gitlab.com"); err != nil || h.Token != "glpat-aaa" || h.OAuth || !h.Expiry.IsZero() {
			t.Errorf("%s: %+v %v", extra, h, err)
		}
	}
}

// FuzzParseOAuth feeds Parse broken configs of OAuth hosts: it must not
// panic, and its errors must keep to errorShape.
func FuzzParseOAuth(f *testing.F) {
	for _, tc := range oauthBadConfigs {
		f.Add([]byte(tc.data))
	}
	f.Add([]byte(oauthSample))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, err := Parse(data, "/x/config.yml")
		checkError(t, data, err)
	})
}
