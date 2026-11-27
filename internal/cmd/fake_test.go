package cmd

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
)

// fakeGitLab returns a runtime whose clients all talk to a test server
// answering with h. Its glab config has gitlab.com (the default host) and
// code.pan.run, both logged in as "me", and it is not inside a git
// checkout.
func fakeGitLab(t *testing.T, h http.HandlerFunc) (rt *cli.Runtime, stdout, stderr *bytes.Buffer) {
	t.Helper()
	for _, env := range []string{"GLCLI_HOST", "GITLAB_HOST", "GLCLI_TOKEN", "GITLAB_TOKEN", "GLCLI_USER", "GLCLI_COMMAND_TIMEOUT_MS"} {
		t.Setenv(env, "")
	}
	gl := httptest.NewServer(h)
	t.Cleanup(gl.Close)
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	rt = cli.New(stdout, stderr)
	rt.Debug = false
	rt.LoadConfig = func() (*config.Config, error) {
		return &config.Config{Path: "config.yml", DefaultHost: "gitlab.com", Hosts: map[string]config.Host{
			"gitlab.com":   {Name: "gitlab.com", APIHost: "gitlab.com", Protocol: "https", Token: "t1", User: "me", TokenSource: "config.yml"},
			"code.pan.run": {Name: "code.pan.run", APIHost: "api.code.pan.run", Protocol: "https", Token: "t2", User: "me", TokenSource: "config.yml"},
		}}, nil
	}
	rt.Remote = func() (string, string, error) { return "", "", errors.New("not a git checkout") }
	rt.NewClient = func(host config.Host) *gitlab.Client {
		c := gitlab.New(host)
		c.BaseURL = gl.URL
		return c
	}
	return rt, stdout, stderr
}
