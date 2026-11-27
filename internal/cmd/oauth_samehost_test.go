package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
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
