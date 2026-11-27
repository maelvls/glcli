package cmd

import (
	"strings"
	"testing"
)

// With GLCLI_DEBUG, every request is traced on stderr, and so is what
// happens after a 401: glab's config read again, the request made again.
// None of these lines has a token in it, old or new, nor the refresh
// token that sits next to them in glab's config.
func TestDebugTraceOfARejectedTokenHasNoToken(t *testing.T) {
	renewed := oauthConfig("oauth-NEW-SECRET", future)
	for _, tc := range []struct {
		name  string
		after string
		code  int
		want  []string
	}{
		{"the token was renewed", renewed, 0, []string{
			"[glcli] GET gitlab.com user: ",
			", HTTP 401\n",
			"[glcli] gitlab.com: the token was rejected: glab's config was read again\n",
			"[glcli] GET gitlab.com user: the token was rejected and there is another one for this host: trying again with it\n",
			", HTTP 200\n",
		}},
		{"the token is the same", oauthConfig("oauth-OLD-SECRET", past), 1, []string{
			", HTTP 401\n",
			"[glcli] gitlab.com: the token was rejected: glab's config was read again\n",
			"glcli: the OAuth token of gitlab.com expired at 2020-01-02T03:04:05Z, and glab's config has no newer one. ",
		}},
		{"the config cannot be read again", "hosts:\n    gitlab.com:\n        oauth2_refresh_token: refresh-SECRET\n        token: \"oauth-NEW-SECRET\n", 1, []string{
			", HTTP 401\n",
			"[glcli] gitlab.com: the token was rejected, and glab's config cannot be read again: reading ",
			"config.yml: invalid YAML near line 4: found unexpected end of stream\n",
			"and glab's config could not be read again to look for a newer one (reading ",
		}},
		{"the new token is for someone else", strings.Replace(renewed, "user: me", "user: someone-else", 1), 1, []string{
			", HTTP 401\n",
			"[glcli] gitlab.com: the token was rejected: glab's config was read again\n",
			"gitlab.com is now logged in as someone-else there",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &gate{accept: "Authorization: Bearer oauth-NEW-SECRET"}
			rt, stdout, stderr, path := fakeGitLabOnDisk(t, g.handler(currentUser), oauthConfig("oauth-OLD-SECRET", past))
			rt.Debug = true
			g.rejecting = func(int) { writeGlabConfig(t, path, tc.after) }
			if code := run(rt, []string{"auth", "test"}); code != tc.code {
				t.Fatalf("exit %d, want %d (stderr: %s)", code, tc.code, stderr.String())
			}
			got := stderr.String()
			at := 0
			for _, want := range tc.want {
				i := strings.Index(got[at:], want)
				if i < 0 {
					t.Fatalf("want %q, after the lines before it, in:\n%s", want, got)
				}
				at += i + len(want)
			}
			for _, not := range []string{"SECRET", "oauth-", "refresh", "Bearer", "PRIVATE"} {
				if strings.Contains(got, not) || strings.Contains(stdout.String(), not) {
					t.Errorf("%q is not expected in:\n%s%s", not, got, stdout.String())
				}
			}
		})
	}
}
