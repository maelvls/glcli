package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
)

// mrWaitState is the merge request g/p!1 as GitLab answers it at one
// moment: what one mr.Fetch reads.
type mrWaitState struct {
	MR          gitlab.MergeRequest
	Versions    []gitlab.Version
	Discussions []gitlab.Discussion
	Approvals   gitlab.Approvals
}

// mrWaitFake is a GitLab with one merge request, g/p!1, for the mr
// commands run through the real entry point. I am "me".
//
// Time is the fake's: every reading of the clock moves it ten seconds
// forward, so a wait never sleeps, and each fetch of the merge request
// sees the next state given to play.
type mrWaitFake struct {
	t    *testing.T
	srv  *httptest.Server
	host string

	mu sync.Mutex
	// release is the version GitLab says it is.
	release string
	// states are what the fetches see, in turn; the last one from then on.
	states []mrWaitState
	// fetched counts the requests per endpoint of the merge request: each
	// fetch asks each of them once.
	fetched map[string]int
	// compare maps "from..to" to the answer of the comparison; a pair
	// that is absent answers 404.
	compare map[string]string
	// asked lists the other requests: "metadata", "hash from..to" (a
	// direct comparison, for the diff), "commits from..to".
	asked []string
	clock time.Time
}

// mrWaitHosts numbers the fake hosts: what a host answered to
// GET /metadata is remembered for as long as the process lives.
var mrWaitHosts atomic.Int64

func newMRWaitFake(t *testing.T, release string) *mrWaitFake {
	t.Helper()
	// What the user's shell may set must not reach the commands.
	for _, env := range []string{"GLCLI_HOST", "GITLAB_HOST", "GLCLI_TOKEN", "GITLAB_TOKEN", "GLCLI_USER", "GLCLI_DEBUG"} {
		t.Setenv(env, "")
	}
	f := &mrWaitFake{
		t:       t,
		host:    fmt.Sprintf("mrwait-%d.example", mrWaitHosts.Add(1)),
		release: release,
		compare: map[string]string{},
		clock:   time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC),
	}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

// play sets what the next fetches of the merge request see, in turn.
func (f *mrWaitFake) play(states ...mrWaitState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states, f.fetched = states, map[string]int{}
}

// fetches is how many times the merge request was fetched since play.
func (f *mrWaitFake) fetches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetched["mr"]
}

// requests returns the requests other than those of the merge request,
// and forgets them.
func (f *mrWaitFake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.asked
	f.asked = nil
	return out
}

func (f *mrWaitFake) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = f.clock.Add(10 * time.Second)
	return f.clock
}

func (f *mrWaitFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	const mrPath = "/api/v4/projects/g/p/merge_requests/1"
	path := r.URL.Path
	var out any
	switch {
	case path == mrPath, strings.HasPrefix(path, mrPath+"/"):
		endpoint := strings.TrimPrefix(strings.TrimPrefix(path, mrPath), "/")
		if endpoint == "" {
			endpoint = "mr"
		}
		n := f.fetched[endpoint]
		f.fetched[endpoint]++
		// A wait that nothing ends would spin for ever on a clock that
		// never makes it sleep: fail it instead.
		if len(f.states) == 0 || n > len(f.states)+50 {
			http.Error(w, `{"message":"mrWaitFake: fetched too many times, the wait does not end"}`, http.StatusBadRequest)
			return
		}
		s := f.states[min(n, len(f.states)-1)]
		switch endpoint {
		case "mr":
			out = s.MR
		case "approvals":
			out = s.Approvals
		case "discussions":
			out = append([]gitlab.Discussion{}, s.Discussions...)
		case "versions":
			out = append([]gitlab.Version{}, s.Versions...)
		default:
			f.asked = append(f.asked, "unexpected "+r.URL.String())
			http.NotFound(w, r)
			return
		}
	case path == "/api/v4/user":
		out = gitlab.User{Username: "me"}
	case path == "/api/v4/metadata":
		f.asked = append(f.asked, "metadata")
		out = map[string]string{"version": f.release}
	case path == "/api/v4/projects/1/repository/compare":
		q := r.URL.Query()
		key := q.Get("from") + ".." + q.Get("to")
		what := "commits "
		if q.Get("straight") == "true" {
			what = "hash "
		}
		f.asked = append(f.asked, what+key)
		answer, ok := f.compare[key]
		if !ok {
			http.Error(w, `{"message":"404 Not found"}`, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(answer))
		return
	default:
		f.asked = append(f.asked, "unexpected "+r.URL.String())
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

// mrWaitEvent is what the tests read of an event.
type mrWaitEvent struct {
	Kind          string `json:"kind"`
	By            string `json:"by"`
	Mine          bool   `json:"mine"`
	SHA           string `json:"sha"`
	PreviousSHA   string `json:"previous_sha"`
	RebaseOnly    *bool  `json:"rebase_only"`
	State         string `json:"state"`
	PreviousState string `json:"previous_state"`
	Comment       *struct {
		Body string `json:"body"`
	} `json:"comment"`
	Commits []struct {
		SHA string `json:"sha"`
	} `json:"commits"`
}

// mrWaitOutput is what the tests read of the JSON of mr get and mr wait.
type mrWaitOutput struct {
	// mr get
	State  string `json:"state"`
	Cursor string `json:"cursor"`
	// mr wait
	MR struct {
		State  string `json:"state"`
		Cursor string `json:"cursor"`
	} `json:"mr"`
	TriggeredBy []string `json:"triggered_by"`
	TimedOut    bool     `json:"timed_out"`
	// both
	Events []mrWaitEvent `json:"events"`
	Hints  []string      `json:"hints"`
	// raw is the line printed.
	raw string
}

// run runs glcli with args against the fake, through the entry point of
// the real command, and decodes what it printed. It fails the test when
// the command fails, or when it took long enough to have slept between
// two polls (--interval is at least 5s).
func (f *mrWaitFake) run(args ...string) mrWaitOutput {
	f.t.Helper()
	var stdout, stderr bytes.Buffer
	rt := cli.New(&stdout, &stderr)
	rt.Now = f.now
	rt.LoadConfig = func() (*config.Config, error) {
		return &config.Config{Path: "test", DefaultHost: f.host, Hosts: map[string]config.Host{
			f.host: {Name: f.host, APIHost: f.host, Protocol: "https", Token: "x", User: "me", TokenSource: "test"},
		}}, nil
	}
	rt.NewClient = func(h config.Host) *gitlab.Client {
		c := gitlab.New(h)
		c.BaseURL = f.srv.URL
		return c
	}
	rt.Remote = func() (string, string, error) { return "", "", errors.New("not in a checkout") }

	start := time.Now()
	code := run(rt, args)
	if took := time.Since(start); took > 4*time.Second {
		f.t.Errorf("glcli %s took %s: it slept for real", strings.Join(args, " "), took)
	}
	if code != 0 {
		f.t.Fatalf("glcli %s: exit %d\nstderr: %s\nstdout: %s", strings.Join(args, " "), code, stderr.String(), stdout.String())
	}
	out := mrWaitOutput{raw: strings.TrimSpace(stdout.String())}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		f.t.Fatalf("glcli %s printed %q: %v", strings.Join(args, " "), stdout.String(), err)
	}
	return out
}

// get runs mr get and returns its cursor.
func (f *mrWaitFake) get(args ...string) mrWaitOutput {
	f.t.Helper()
	out := f.run(append([]string{"mr", "get", "g/p!1"}, args...)...)
	if out.Cursor == "" {
		f.t.Fatalf("mr get printed no cursor: %s", out.raw)
	}
	return out
}

// wait runs mr wait from cursor, polling every 5 seconds of the fake's
// time.
func (f *mrWaitFake) wait(cursor string, args ...string) mrWaitOutput {
	f.t.Helper()
	return f.run(append([]string{"mr", "wait", "g/p!1", "--interval", "5s", "--cursor", cursor}, args...)...)
}

func mrWaitAt(min int) time.Time { return time.Date(2026, 11, 26, 10, min, 0, 0, time.UTC) }

const (
	mrWaitPatchA = "d504412d5b6e6739647e752aff8e468dde093f2f"
	mrWaitPatchB = "72c30d1f0115fc1d2bb0b29b24dc2982cbcdfd32"
)

// mrWaitOpened is g/p!1 by alice, opened, at head1 on base1 with patch
// A, with one comment of mine.
func mrWaitOpened() mrWaitState {
	s := mrWaitState{
		MR: gitlab.MergeRequest{
			ID: 100, IID: 1, ProjectID: 1, Title: "Add a thing", State: "opened", SHA: "head1",
			Author: gitlab.User{Username: "alice"}, SourceBranch: "thing", TargetBranch: "main",
			WebURL: "https://gitlab.example/g/p/-/merge_requests/1", CreatedAt: mrWaitAt(0), UpdatedAt: mrWaitAt(1),
		},
		Versions: []gitlab.Version{{ID: 1, HeadCommitSHA: "head1", BaseCommitSHA: "base1", CreatedAt: mrWaitAt(0), PatchIDSHA: mrWaitPatchA}},
	}
	s.MR.References.Full = "g/p!1"
	return s.note(10, "me", 1, "please fix", false)
}

// note adds a note to a copy of s, as a thread of its own.
func (s mrWaitState) note(id int, user string, min int, body string, system bool) mrWaitState {
	n := gitlab.Note{ID: id, Author: gitlab.User{Username: user}, CreatedAt: mrWaitAt(min), UpdatedAt: mrWaitAt(min), Body: body, System: system}
	s.Discussions = append(append([]gitlab.Discussion{}, s.Discussions...), gitlab.Discussion{ID: fmt.Sprintf("discussion-%012d", id), IndividualNote: true, Notes: []gitlab.Note{n}})
	return s
}

// push returns s after alice pushed head on base: a new version with
// GitLab's patch ID, and the system note of the push.
func (s mrWaitState) push(id int, head, base, patch string, min int) mrWaitState {
	s.MR.SHA = head
	s.Versions = append([]gitlab.Version{{ID: id, HeadCommitSHA: head, BaseCommitSHA: base, CreatedAt: mrWaitAt(min), PatchIDSHA: patch}}, s.Versions...)
	return s.note(1000+id, "alice", min, "added 1 commit\n\n<ul><li>abc - fix</li></ul>", true)
}

// state returns s in another state.
func (s mrWaitState) state(state string) mrWaitState {
	s.MR.State = state
	return s
}

// mrWaitComparison is the answer of the compare API for one file, a.go.
func mrWaitComparison(diff string) string {
	b, _ := json.Marshal(map[string]any{
		"commits": []map[string]string{{"short_id": "c1", "title": "fix"}},
		"diffs":   []map[string]any{{"old_path": "a.go", "new_path": "a.go", "a_mode": "100644", "b_mode": "100644", "diff": diff}},
	})
	return string(b)
}
