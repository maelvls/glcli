package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maelvls/glcli/internal/config"
)

// A config written by hand, with the keys glab writes for an OAuth login
// (see internal/config/oauth_test.go for where they come from).
const headerSample = `
host: gitlab.com
hosts:
    gitlab.com:
        user: me
        is_oauth2: "true"
        oauth2_refresh_token: refresh-secret
        oauth2_expiry_date: "2026-11-27T19:42:10+01:00"
        token: oauth-secret
    code.pan.run:
        token: glpat-secret
        user: other
    old.example.com:
        is_oauth2: "false"
        token: glpat-old
`

// The header each kind of token is sent in: "Authorization: Bearer" for
// the token of a host glab logged in to with OAuth, PRIVATE-TOKEN for
// every other token, as before. The rules are GitLab's
// (https://docs.gitlab.com/api/rest/authentication/): an OAuth 2.0 token
// goes in the access_token parameter or the Authorization header, a
// personal, project or group access token in PRIVATE-TOKEN.
func TestHeaderOfEachKindOfToken(t *testing.T) {
	type sent struct{ private, authorization string }
	var got sent
	requests := 0
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		got = sent{r.Header.Get("PRIVATE-TOKEN"), r.Header.Get("Authorization")}
		if len(r.Header.Values("PRIVATE-TOKEN"))+len(r.Header.Values("Authorization")) != 1 {
			t.Errorf("want one header with the token, got %v", r.Header)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("the token must not be in the URL, got %s", r.URL)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer gl.Close()

	for _, tc := range []struct {
		name, host string
		env        map[string]string
		want       sent
	}{
		{name: "OAuth login of glab", host: "gitlab.com", want: sent{authorization: "Bearer oauth-secret"}},
		{name: "personal access token in glab's config", host: "code.pan.run", want: sent{private: "glpat-secret"}},
		{name: "is_oauth2 false", host: "old.example.com", want: sent{private: "glpat-old"}},
		{name: "$GLCLI_TOKEN on a host glab logged in to with OAuth", host: "gitlab.com", env: map[string]string{"GLCLI_TOKEN": "glpat-env"}, want: sent{private: "glpat-env"}},
		{name: "$GITLAB_TOKEN on a host glab logged in to with OAuth", host: "gitlab.com", env: map[string]string{"GITLAB_TOKEN": "glpat-env"}, want: sent{private: "glpat-env"}},
		{name: "$GLCLI_TOKEN on a default host glab does not know", host: "new.example.com", env: map[string]string{"GLCLI_HOST": "new.example.com", "GLCLI_TOKEN": "glpat-env"}, want: sent{private: "glpat-env"}},
		{name: "$GLCLI_TOKEN is not for another host than the default one", host: "code.pan.run", env: map[string]string{"GLCLI_TOKEN": "glpat-env"}, want: sent{private: "glpat-secret"}},
		{name: "OAuth host that is not the default one, with $GLCLI_TOKEN set", host: "gitlab.com", env: map[string]string{"GLCLI_HOST": "code.pan.run", "GLCLI_TOKEN": "glpat-env"}, want: sent{authorization: "Bearer oauth-secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, env := range []string{"GLCLI_HOST", "GITLAB_HOST", "GLCLI_TOKEN", "GITLAB_TOKEN"} {
				t.Setenv(env, tc.env[env])
			}
			cfg, err := config.Parse([]byte(headerSample), "/x/config.yml")
			if err != nil {
				t.Fatal(err)
			}
			h, err := cfg.Host(tc.host)
			if err != nil {
				t.Fatal(err)
			}
			c := New(h)
			c.BaseURL = gl.URL
			got, requests = sent{}, 0
			var out map[string]any
			if err := c.Get(context.Background(), "user", nil, &out); err != nil {
				t.Fatal(err)
			}
			if got != tc.want || requests != 1 {
				t.Errorf("got %+v in %d request(s), want %+v in one", got, requests, tc.want)
			}
		})
	}
}

// Without any of glab's OAuth keys, a host is built as it always was: its
// token goes in PRIVATE-TOKEN.
func TestHeaderOfAPlainHost(t *testing.T) {
	var private, authorization string
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		private, authorization = r.Header.Get("PRIVATE-TOKEN"), r.Header.Get("Authorization")
		fmt.Fprint(w, `{}`)
	}))
	defer gl.Close()
	c := New(config.Host{Name: "test", Token: "secret"})
	c.BaseURL = gl.URL
	var out map[string]any
	if err := c.Get(context.Background(), "user", nil, &out); err != nil {
		t.Fatal(err)
	}
	if private != "secret" || authorization != "" {
		t.Errorf("got PRIVATE-TOKEN %q and Authorization %q", private, authorization)
	}
}
