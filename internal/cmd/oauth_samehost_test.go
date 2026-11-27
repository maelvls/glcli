package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// What a command does not take from glab's config when it reads it again
// after a 401: anything but the token of the same host, the same API
// address and the same user. As elsewhere, the config is written by hand.

// emptyInbox answers what "inbox" asks for when nothing waits on me.
func emptyInbox(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, `[]`)
}

// "inbox wait" asks for the default host at each poll. When the token of
// that host is renewed while "host:" changed too in glab's config, the
// wait goes on with the new token on the host it started on: it does not
// move to the new default host.
func TestInboxWaitStaysOnItsHostWhenTheDefaultHostChanges(t *testing.T) {
	old, renewed := "Authorization: Bearer oauth-OLD-SECRET", "Authorization: Bearer oauth-NEW-SECRET"
	g := &gate{accept: old}
	var path string
	var requests atomic.Int32
	rt, stdout, stderr, p := fakeGitLabOnDisk(t, g.handler(func(w http.ResponseWriter, r *http.Request) {
		emptyInbox(w, r)
		// After the first poll: glab renewed the token of gitlab.com, and
		// the default host is now code.pan.run.
		if requests.Add(1) == 3 {
			writeGlabConfig(t, path, strings.Replace(oauthConfig("oauth-NEW-SECRET", future), "host: gitlab.com", "host: code.pan.run", 1))
			g.setAccept(renewed)
		}
	}), oauthConfig("oauth-OLD-SECRET", future))
	path = p
	minuteClock(rt)
	if code := run(rt, []string{"inbox", "wait", "--interval", "10s", "--timeout", "30m"}); code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
	}
	var got struct {
		Me, Host string
		TimedOut bool `json:"timed_out"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got.Host != "gitlab.com" || got.Me != "me" || !got.TimedOut {
		t.Errorf("want the result of a wait on gitlab.com that timed out, got %s (%v)", stdout.String(), err)
	}
	if stderr.Len() != 0 {
		t.Errorf("nothing expected on stderr, got %s", stderr.String())
	}
	seen := g.requests()
	if len(seen) < 9 {
		t.Fatalf("want at least three polls, got %q", seen)
	}
	// The token of code.pan.run ("PRIVATE-TOKEN: glpat-SECRET") is never
	// sent: every request is for gitlab.com, with its old token, then
	// with its new one.
	if i := slices.IndexFunc(seen, func(s string) bool { return s != old && s != renewed }); i >= 0 {
		t.Errorf("request %d carried %q: the wait moved to another host (%q)", i+1, seen[i], seen)
	}
	if last := seen[len(seen)-1]; last != renewed {
		t.Errorf("the last request carried %q, want the new token", last)
	}
}

// After the 401, glab's config has another token for the host, but for
// another API address or another user than the command started with: the
// token is not used. The command ends with its 401, after one request,
// and says to run it again.
func TestRenewedTokenForAnotherLoginIsNotUsed(t *testing.T) {
	renewed := oauthConfig("oauth-NEW-SECRET", future)
	for _, tc := range []struct {
		name  string
		after string
		want  string
	}{
		{"another API host",
			strings.Replace(renewed, "api_protocol: https", "api_protocol: https\n        api_host: new-api.example.com", 1),
			"the API of gitlab.com is now at https://new-api.example.com there"},
		{"another API protocol",
			strings.Replace(renewed, "api_protocol: https", "api_protocol: http", 1),
			"the API of gitlab.com is now at http://gitlab.com there"},
		{"another user",
			strings.Replace(renewed, "user: me", "user: someone-else", 1),
			"gitlab.com is now logged in as someone-else there"},
		{"another user, as a personal access token",
			strings.Replace(patConfig("glpat-NEW-SECRET"), "user: me", "user: someone-else", 1),
			"gitlab.com is now logged in as someone-else there"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// GitLab would take the new token: it must not get it.
			g := &gate{accept: "Authorization: Bearer oauth-NEW-SECRET"}
			rt, stdout, stderr, path := fakeGitLabOnDisk(t, g.handler(currentUser), oauthConfig("oauth-OLD-SECRET", past))
			if tc.after == renewed {
				t.Fatal("the config of the test did not change")
			}
			g.rejecting = func(int) { writeGlabConfig(t, path, tc.after) }
			if code := run(rt, []string{"auth", "test"}); code != 1 {
				t.Fatalf("exit %d, want 1 (stdout: %s, stderr: %s)", code, stdout.String(), stderr.String())
			}
			want := "glcli: GitLab GET user: HTTP 401: 401 Unauthorized\n" +
				"glcli: the token was rejected, and glab's config changed since this command started: " + tc.want +
				". Run the same glcli command again: it will use the new login from its start\n"
			if got := stderr.String(); got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
			if got, want := g.requests(), []string{"Authorization: Bearer oauth-OLD-SECRET"}; !slices.Equal(got, want) {
				t.Errorf("requests:\n got %q\nwant %q", got, want)
			}
		})
	}
}

// The same in the middle of "inbox wait", which asks for "my" merge
// requests with the username it started with: with the token of someone
// else it would print the inbox of the first user, as seen by the second.
func TestInboxWaitEndsWhenTheHostIsLoggedInAsSomeoneElse(t *testing.T) {
	old := "Authorization: Bearer oauth-OLD-SECRET"
	g := &gate{accept: old}
	var path string
	var requests atomic.Int32
	rt, stdout, stderr, p := fakeGitLabOnDisk(t, g.handler(func(w http.ResponseWriter, r *http.Request) {
		emptyInbox(w, r)
		if requests.Add(1) == 3 {
			writeGlabConfig(t, path, strings.Replace(oauthConfig("oauth-NEW-SECRET", future), "user: me", "user: someone-else", 1))
			g.setAccept("Authorization: Bearer oauth-NEW-SECRET")
		}
	}), oauthConfig("oauth-OLD-SECRET", future))
	path = p
	minuteClock(rt)
	if code := run(rt, []string{"inbox", "wait", "--interval", "10s", "--timeout", "30m"}); code != 1 {
		t.Fatalf("exit %d, want 1 (stdout: %s, stderr: %s)", code, stdout.String(), stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "HTTP 401: 401 Unauthorized\n") || !strings.Contains(got, "gitlab.com is now logged in as someone-else there. Run the same glcli command again") || strings.Contains(got, "SECRET") {
		t.Errorf("got:\n%s", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("nothing expected on stdout, got %s", stdout.String())
	}
	if i := slices.IndexFunc(g.requests(), func(s string) bool { return s != old }); i >= 0 {
		t.Errorf("request %d carried %q: the token of someone else was used", i+1, g.requests()[i])
	}
}

// A config that does not name the user, on either side, says nothing
// about a change of user: the new token is used.
func TestRenewedTokenWithoutAUserIsUsed(t *testing.T) {
	withUser := oauthConfig("oauth-NEW-SECRET", future)
	withoutUser := strings.Replace(withUser, "        user: me\n", "", 1)
	for name, configs := range map[string][2]string{
		"no user after":  {oauthConfig("oauth-OLD-SECRET", past), withoutUser},
		"no user before": {strings.Replace(oauthConfig("oauth-OLD-SECRET", past), "        user: me\n", "", 1), withUser},
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Count(configs[0], "user: me")+strings.Count(configs[1], "user: me") != 3 {
				t.Fatal("the configs of the test are not the ones meant")
			}
			g := &gate{accept: "Authorization: Bearer oauth-NEW-SECRET"}
			rt, stdout, stderr, path := fakeGitLabOnDisk(t, g.handler(currentUser), configs[0])
			g.rejecting = func(int) { writeGlabConfig(t, path, configs[1]) }
			if code := run(rt, []string{"auth", "test"}); code != 0 {
				t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
			}
			if got := stdout.String(); got != `{"host":"gitlab.com","username":"me","name":"Me"}`+"\n" {
				t.Errorf("got %s", got)
			}
		})
	}
}

// When glab's config cannot be read again after a 401, nothing is known
// of a newer token: the hint says that the file could not be read, and
// why, instead of saying that it has no newer token. The reason is the
// one glcli gives for such a file at any time: the file, a line, the kind
// of problem, and nothing of what the file holds.
func TestRejectedTokenHintSaysThatTheConfigCouldNotBeReadAgain(t *testing.T) {
	// Tokens in every place the yaml package could quote.
	secrets := "oauth-NEW-SECRET"
	for _, tc := range []struct {
		name, broken, reason string
	}{
		{"unclosed quote", "hosts:\n    gitlab.com:\n        token: \"" + secrets + "\n", "invalid YAML near line 3: found unexpected end of stream"},
		{"bad indentation", "hosts:\n    gitlab.com:\n        token: " + secrets + "\n      user: me\n", "invalid YAML near line 1: did not find expected key"},
		{"tabs", "hosts:\n\tgitlab.com:\n\t\ttoken: " + secrets + "\n", "invalid YAML near line 2: found character that cannot start any token"},
		{"unknown anchor", "hosts:\n    gitlab.com:\n        token: *" + secrets + "\n", "invalid YAML"},
		{"token as the file", secrets + "\n", "line 1: the file is not a mapping"},
		{"token as hosts", "hosts: " + secrets + "\n", "line 1: hosts is not a mapping"},
		{"token as a host", "hosts:\n    gitlab.com: " + secrets + "\n", "line 2: a host is not a mapping"},
		{"list as token", "hosts:\n    gitlab.com:\n        token: [" + secrets + "]\n", "line 3: token is not a string"},
		{"duplicate token", "hosts:\n    gitlab.com:\n        token: " + secrets + "\n        token: " + secrets + "\n", "line 4: duplicate key (first at line 3)"},
		{"not UTF-8", "hosts:\n    gitlab.com:\n        token: " + secrets + "\xff\n", "invalid YAML: invalid leading UTF-8 octet"},
	} {
		for kind, before := range map[string]string{"OAuth token": oauthConfig("oauth-OLD-SECRET", past), "personal access token": patConfig("glpat-OLD-SECRET")} {
			t.Run(tc.name+", "+kind, func(t *testing.T) {
				g := &gate{accept: "nothing"}
				rt, stdout, stderr, path := fakeGitLabOnDisk(t, g.handler(currentUser), before)
				g.rejecting = func(int) { writeGlabConfig(t, path, tc.broken) }
				if code := run(rt, []string{"auth", "test"}); code != 1 {
					t.Fatalf("exit %d, want 1 (stdout: %s, stderr: %s)", code, stdout.String(), stderr.String())
				}
				unread := ", and glab's config could not be read again to look for a newer one (reading " + path + ": " + tc.reason + ")"
				hint := "glcli: the OAuth token of gitlab.com expired at 2020-01-02T03:04:05Z" + unread + ". glcli never renews it; glab does, when it calls GitLab: run `glab auth status --hostname gitlab.com`"
				if kind == "personal access token" {
					hint = "glcli: the token was rejected" + unread + ": run `glab auth login --hostname gitlab.com`"
				}
				got := stderr.String()
				if !strings.HasPrefix(got, "glcli: GitLab GET user: HTTP 401: 401 Unauthorized\n"+hint) || strings.Count(got, "\n") != 2 {
					t.Errorf("got:\n%s\nwant the 401, then a hint that starts with:\n%s", got, hint)
				}
				for _, not := range []string{"SECRET", "oauth-", "glpat", "refresh", "has no newer one"} {
					if strings.Contains(got, not) {
						t.Errorf("%q is not expected in:\n%s", not, got)
					}
				}
				if n := len(g.requests()); n != 1 {
					t.Errorf("%d requests, want 1", n)
				}
			})
		}
	}
}

// The same when the file cannot be opened at all: here a directory took
// its place.
func TestRejectedTokenHintWhenTheConfigIsNotAFile(t *testing.T) {
	g := &gate{accept: "nothing"}
	rt, _, stderr, path := fakeGitLabOnDisk(t, g.handler(currentUser), oauthConfig("oauth-OLD-SECRET", past))
	g.rejecting = func(int) {
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Error(err)
		}
	}
	if code := run(rt, []string{"auth", "test"}); code != 1 {
		t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
	}
	got := stderr.String()
	if !strings.Contains(got, "expired at 2020-01-02T03:04:05Z, and glab's config could not be read again to look for a newer one (read "+path+": ") || strings.Contains(got, "has no newer one") || strings.Contains(got, "SECRET") {
		t.Errorf("got:\n%s", got)
	}
}

// A config that is gone is a config without hosts, as when glcli starts:
// it was read, and has no newer token.
func TestRejectedTokenHintWhenTheConfigIsGone(t *testing.T) {
	g := &gate{accept: "nothing"}
	rt, _, stderr, path := fakeGitLabOnDisk(t, g.handler(currentUser), oauthConfig("oauth-OLD-SECRET", past))
	g.rejecting = func(int) {
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
	}
	if code := run(rt, []string{"auth", "test"}); code != 1 {
		t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "expired at 2020-01-02T03:04:05Z, and glab's config has no newer one. ") || strings.Contains(got, "could not be read") {
		t.Errorf("got:\n%s", got)
	}
}
