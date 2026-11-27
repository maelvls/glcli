package ref

import (
	"os/exec"
	"strings"
	"testing"
)

const remoteSecret = "glpat-REMOTESECRET"

// Remotes that ParseRemote cannot read, with a token in them, and what the
// error shows of each.
var unreadableRemotes = []struct{ url, shown string }{
	// A colon where the slash should be: url.Parse fails.
	{"https://oauth2:" + remoteSecret + "@gitlab.com:group/project.git", `"https://gitlab.com:group/project.git"`},
	// No project.
	{"https://oauth2:" + remoteSecret + "@gitlab.com", `"https://gitlab.com"`},
	{"https://oauth2:" + remoteSecret + "@gitlab.com/", `"https://gitlab.com/"`},
	{"https://" + remoteSecret + "@gitlab.com", `"https://gitlab.com"`},
	{"ssh://" + remoteSecret + "@gitlab.com", `"ssh://gitlab.com"`},
	// No scheme.
	{"oauth2:" + remoteSecret + "@gitlab.com:group/project.git", `"gitlab.com:group/project.git"`},
	{"oauth2:" + remoteSecret + "@gitlab.com", `"gitlab.com"`},
	{"git@" + remoteSecret + "@gitlab.com:group/project.git", `"gitlab.com:group/project.git"`},
	// A password with a slash or an "@" in it, a token as the scheme.
	{"https://oauth2:" + remoteSecret + "/x@gitlab.com:group/project.git", `"https://gitlab.com:group/project.git"`},
	{"https://oauth2:p@" + remoteSecret + "@gitlab.com:group/project.git", `"https://gitlab.com:group/project.git"`},
	{remoteSecret + "://oauth2@gitlab.com", `"gitlab.com"`},
}

func TestRemoteErrorHidesCredentials(t *testing.T) {
	for _, tc := range unreadableRemotes {
		if h, p, ok := ParseRemote(tc.url); ok {
			t.Errorf("ParseRemote(%q) = %q %q: expected a failure", tc.url, h, p)
			continue
		}
		msg := remoteError(tc.url).Error()
		if strings.Contains(msg, "glpat") || strings.Contains(msg, "SECRET") {
			t.Errorf("%q: the error shows the token: %s", tc.url, msg)
		}
		if want := "cannot read the remote URL " + tc.shown + " (what precedes the @ is left out)"; msg != want {
			t.Errorf("%q:\n got %s\nwant %s", tc.url, msg, want)
		}
	}
}

// Without an "@" there is nothing to hide: the URL is shown as it is.
func TestRemoteErrorWithoutCredentials(t *testing.T) {
	for _, url := range []string{"https://gitlab.com", "not a remote", ""} {
		if got, want := remoteError(url).Error(), `cannot read the remote URL "`+url+`"`; got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
}

// A remote that can be read never gives its credentials as host or project.
func TestParseRemoteDropsCredentials(t *testing.T) {
	for _, url := range []string{
		"https://oauth2:" + remoteSecret + "@gitlab.com/group/project.git",
		"https://" + remoteSecret + "@gitlab.com/group/project.git",
		"ssh://" + remoteSecret + "@gitlab.com:2222/group/project.git",
		remoteSecret + "@gitlab.com:group/project.git",
	} {
		h, p, ok := ParseRemote(url)
		if !ok || h != "gitlab.com" || p != "group/project" {
			t.Errorf("ParseRemote(%q) = %q %q %v", url, h, p, ok)
		}
	}
}

// The same through git itself: a checkout whose origin holds a token, and
// "!1" read from inside it.
func TestGitRemoteHidesCredentials(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	// The remote must be the one written below, not one that the git
	// config of the machine rewrites (url.….insteadOf).
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	for _, tc := range unreadableRemotes {
		dir := t.TempDir()
		for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", tc.url}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %s: %v: %s", args[0], err, out)
			}
		}
		t.Chdir(dir)
		_, _, err := GitRemote()
		if err == nil || !strings.Contains(err.Error(), tc.shown) {
			t.Errorf("%q: got %v, want an error with %s", tc.url, err, tc.shown)
		}
		_, err2 := ParseMR("!1", Defaults{Host: "gitlab.com", Remote: GitRemote})
		for _, err := range []error{err, err2} {
			if err == nil {
				t.Errorf("%q: expected an error", tc.url)
			} else if msg := err.Error(); strings.Contains(msg, "glpat") || strings.Contains(msg, "SECRET") {
				t.Errorf("%q: the error shows the token: %s", tc.url, msg)
			}
		}
	}
}
