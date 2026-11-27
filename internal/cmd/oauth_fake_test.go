package cmd

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/config"
)

// fakeGitLabOnDisk is fakeGitLab with a glab config that is a real file,
// written by hand (no glab was run), and read the way glcli reads glab's:
// tests can change the file while a command runs, as glab does when it
// renews a token. It returns the path of the file too.
func fakeGitLabOnDisk(t *testing.T, h http.HandlerFunc, data string) (rt *cli.Runtime, stdout, stderr *bytes.Buffer, path string) {
	t.Helper()
	rt, stdout, stderr = fakeGitLab(t, h)
	dir := t.TempDir()
	t.Setenv("GLAB_CONFIG_DIR", dir)
	path = filepath.Join(dir, "config.yml")
	writeGlabConfig(t, path, data)
	rt.LoadConfig = config.Load
	return rt, stdout, stderr, path
}

// writeGlabConfig replaces the file in one step, as glab does (it renames
// a temporary file over config.yml): a reader never sees half a file.
func writeGlabConfig(t *testing.T, path, data string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// oauthConfig is a glab config with gitlab.com (the default host) logged
// in to with OAuth, and code.pan.run with a personal access token. The
// keys are the ones glab writes (see internal/config/oauth_test.go).
func oauthConfig(token, expiry string) string {
	return `
host: gitlab.com
hosts:
    gitlab.com:
        api_protocol: https
        user: me
        is_oauth2: "true"
        oauth2_refresh_token: refresh-SECRET
        oauth2_expiry_date: "` + expiry + `"
        token: ` + token + `
    code.pan.run:
        token: glpat-SECRET
        user: me
`
}
