package cmd

import (
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// updateFollowGolden rewrites the golden files of the default mode. They
// were written by the code before --follow existed: rewrite them only on
// purpose.
var updateFollowGolden = flag.Bool("update-follow-golden", false, "rewrite testdata/follow-default")

// checkGolden compares what a run printed (exit code, stdout, stderr)
// with testdata/follow-default/name.
func checkGolden(t *testing.T, name string, r followRun) {
	t.Helper()
	got := "exit " + strconv.Itoa(r.code) + "\n--- stdout\n" + r.stdout + "--- stderr\n" + r.stderr
	path := filepath.Join("testdata", "follow-default", name)
	if *updateFollowGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s changed without --follow:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// Without --follow, mr wait prints what it printed before --follow
// existed, byte for byte, on the same fakes: a comment that ends the
// wait, a timeout, a merge of mine (no trigger, a hint), and an outage
// (a note on stderr).
func TestMRWaitDefaultModeUnchanged(t *testing.T) {
	f := newMRWaitFake(t, "18.5.1-ee")
	opened := mrWaitOpened()
	f.play(opened)
	start := f.get().Cursor

	mine := opened.note(11, "me", 2, "one more thing", false)
	commented := mine.note(12, "bob", 3, "done, have a look", false)
	f.play(opened, mine, commented)
	checkGolden(t, "mr-comment", mrFollow(t, f, nil, "--cursor", start))

	f.play(opened)
	checkGolden(t, "mr-timeout", mrFollow(t, f, nil, "--cursor", start, "--timeout", "1m"))

	merged := opened.state("merged")
	merged.MR.MergeUser = &gitlab.User{Username: "me"}
	f.play(opened, merged)
	checkGolden(t, "mr-merged-by-me", mrFollow(t, f, nil, "--cursor", start))

	gate := &followGate{match: isMRFetch, fail: func(n int) int {
		if n == 2 || n == 3 {
			return http.StatusServiceUnavailable
		}
		return 0
	}}
	f.play(opened, commented)
	checkGolden(t, "mr-outage", mrFollow(t, f, gate, "--cursor", start))
}

// Without --follow, inbox wait prints what it printed before --follow
// existed, byte for byte: something new, a timeout, a warning.
func TestInboxWaitDefaultModeUnchanged(t *testing.T) {
	args := []string{"inbox", "wait", "--role", "review", "--since", waitSince, "--interval", "1m", "--timeout", "14m"}
	for _, tt := range []struct {
		name string
		g    *waitGitLab
	}{
		{"inbox-new", &waitGitLab{mrs: 1, replyAt: 2}},
		{"inbox-timeout", &waitGitLab{mrs: 1}},
		{"inbox-warning", &waitGitLab{mrs: 2, failing: func(int) bool { return true }}},
	} {
		rt, stdout, stderr := inboxFollowRuntime(t, tt.g, nil)
		checkGolden(t, tt.name, runFollow(t, rt, stdout, stderr, args...))
	}
	g := &waitGitLab{mrs: 1, replyAt: 3}
	gate := &followGate{match: isInboxList, fail: func(n int) int {
		if n == 2 {
			return http.StatusBadGateway
		}
		return 0
	}}
	rt, stdout, stderr := inboxFollowRuntime(t, g, gate)
	checkGolden(t, "inbox-outage", runFollow(t, rt, stdout, stderr, args...))
}
