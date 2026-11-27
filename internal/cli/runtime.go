// Package cli holds what every command shares: configuration, GitLab
// clients, the current user, JSON output with warnings and hints, and the
// polling loop of the wait commands.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
	"github.com/maelvls/glcli/internal/output"
	"github.com/maelvls/glcli/internal/ref"
)

// Runtime is shared by the commands of one invocation.
type Runtime struct {
	Stdout   io.Writer
	Stderr   io.Writer
	HostFlag string
	RepoFlag string
	Debug    bool
	Now      func() time.Time
	// LoadConfig and Remote can be replaced in tests.
	LoadConfig func() (*config.Config, error)
	Remote     func() (host, project string, err error)
	// NewClient can be replaced in tests.
	NewClient func(h config.Host) *gitlab.Client

	mu       sync.Mutex
	cfg      *config.Config
	reread   *config.Config // the config as last read again after a rejected token
	reloads  int            // how many times it was read again
	clients  map[string]*gitlab.Client
	me       map[string]string
	warnings []string
	hints    []string

	// reloadMu makes the goroutines whose token was rejected read glab's
	// config again one at a time. It is taken before mu, never after.
	reloadMu sync.Mutex
}

// New returns a runtime writing to stdout and stderr.
func New(stdout, stderr io.Writer) *Runtime {
	return &Runtime{
		Stdout:     stdout,
		Stderr:     stderr,
		Debug:      os.Getenv("GLCLI_DEBUG") != "",
		Now:        time.Now,
		LoadConfig: config.Load,
		Remote:     ref.GitRemote,
		NewClient:  gitlab.New,
	}
}

// Config loads glab's configuration once: the hosts, the default host and
// the users of a command are the ones it started with, however long it
// runs. (The file is read again when GitLab rejects a token that came from
// it, to look for the token that replaced it and for nothing else: see
// renewer.)
func (r *Runtime) Config() (*config.Config, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cfg == nil {
		cfg, err := r.LoadConfig()
		if err != nil {
			return nil, err
		}
		r.cfg = cfg
	}
	return r.cfg, nil
}

// DefaultHost is --host, or the default host of the configuration.
func (r *Runtime) DefaultHost() (string, error) {
	if r.HostFlag != "" {
		return r.HostFlag, nil
	}
	cfg, err := r.Config()
	if err != nil {
		return "", err
	}
	return cfg.Default(), nil
}

// Client returns the client of host ("" for the default host).
func (r *Runtime) Client(host string) (*gitlab.Client, error) {
	if host == "" {
		var err error
		if host, err = r.DefaultHost(); err != nil {
			return nil, err
		}
	}
	cfg, err := r.Config()
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[host]; ok {
		return c, nil
	}
	h, err := cfg.Host(host)
	if err != nil {
		return nil, err
	}
	c := r.NewClient(h)
	if r.Debug {
		c.Trace = r.Tracef
	}
	// A token from the environment does not change while glcli runs: only
	// the ones of glab's config are looked for again.
	if !strings.HasPrefix(h.TokenSource, "$") {
		c.Renewer = renewer{r}
	}
	if r.clients == nil {
		r.clients = map[string]*gitlab.Client{}
	}
	r.clients[host] = c
	return c, nil
}

// renewer is the gitlab.Renewer of the clients whose token came from glab's
// config. glcli never renews a token itself and never writes to glab's
// config: when GitLab rejects a token, it reads the config again, in case
// glab renewed the token meanwhile (it does so for an OAuth login whenever
// it calls GitLab with a token that has expired or is about to), or the
// user logged in again.
type renewer struct{ r *Runtime }

func (n renewer) Generation() int {
	n.r.mu.Lock()
	defer n.r.mu.Unlock()
	return n.r.reloads
}

// Renew returns the host with the token that glab's config, read again,
// has for it, when that is another token than the rejected one.
//
// What is read again is kept apart from the config the command started
// with, and only its tokens are looked at: a default host that changed in
// the file meanwhile does not send the rest of an "inbox wait" to another
// instance.
//
// The requests of a command go in bursts (mr.Fetch and inbox.Collect send
// several at once), and a rejected token is rejected for each: the config
// is read once for a burst, not once per request. The first goroutine to
// come reads it; the ones that waited meanwhile find either another token
// than theirs in what was read, or, by the generation, that the config was
// read after their request left, and so do not read it again. A request
// that leaves after that and is rejected too makes the config be read
// again: a command that waits looks again at each poll.
func (n renewer) Renew(rejected config.Host, generation int) (config.Host, bool) {
	r := n.r
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	r.mu.Lock()
	reread, reloads := r.reread, r.reloads
	r.mu.Unlock()
	if h, ok := otherToken(reread, rejected); ok {
		return h, true
	}
	if reloads != generation {
		return rejected, false
	}
	fresh, err := r.LoadConfig()
	r.mu.Lock()
	r.reloads++
	if err == nil {
		r.reread = fresh
	}
	r.mu.Unlock()
	if err != nil {
		r.Tracef("%s: the token was rejected, and glab's config cannot be read again: %v", rejected.Name, err)
		return rejected, false
	}
	r.Tracef("%s: the token was rejected: glab's config was read again", rejected.Name)
	return otherToken(fresh, rejected)
}

// otherToken returns the host of cfg that rejected came from, when the file
// has another token for it than the rejected one. The tokens of the
// environment are not looked at: cfg may name another default host than
// the config the command started with, and they are for the default host.
func otherToken(cfg *config.Config, rejected config.Host) (config.Host, bool) {
	if cfg == nil {
		return rejected, false
	}
	h, ok := cfg.Hosts[rejected.Name]
	if !ok || h.Token == "" || h.Token == rejected.Token {
		return rejected, false
	}
	return h, true
}

// Me returns the username of the token's owner on c's host: the one glab
// recorded, or else GET /user.
func (r *Runtime) Me(ctx context.Context, c *gitlab.Client) (string, error) {
	r.mu.Lock()
	if u, ok := r.me[c.Host.Name]; ok {
		r.mu.Unlock()
		return u, nil
	}
	r.mu.Unlock()
	u := os.Getenv("GLCLI_USER")
	if u == "" && c.Host.TokenSource != "" && !strings.HasPrefix(c.Host.TokenSource, "$") {
		u = c.Host.User
	}
	if u == "" {
		var user gitlab.User
		if err := c.Get(ctx, "user", nil, &user); err != nil {
			return "", err
		}
		u = user.Username
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.me == nil {
		r.me = map[string]string{}
	}
	r.me[c.Host.Name] = u
	return u, nil
}

// MR parses a merge request reference with the runtime's defaults.
func (r *Runtime) MR(s string) (ref.MR, error) {
	host, err := r.DefaultHost()
	if err != nil {
		return ref.MR{}, err
	}
	return ref.ParseMR(s, ref.Defaults{Host: host, HostFlag: r.HostFlag, Repo: r.RepoFlag, Remote: r.Remote})
}

// Tracef writes a debug line on stderr when GLCLI_DEBUG is set.
func (r *Runtime) Tracef(format string, args ...any) {
	if r.Debug {
		fmt.Fprintf(r.Stderr, "[glcli] "+format+"\n", args...)
	}
}

// Warnf adds a warning to the next printed result.
func (r *Runtime) Warnf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

// ResetWarnings forgets the warnings not printed yet: a wait command calls
// it before each poll, so that only the last poll's warnings are printed.
func (r *Runtime) ResetWarnings() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnings = nil
}

// Hintf adds a hint (what to do next) to the next printed result.
func (r *Runtime) Hintf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hints = append(r.hints, fmt.Sprintf(format, args...))
}

// Notef writes a progress note on stderr (for the wait commands, whose
// stdout is only the final result).
func (r *Runtime) Notef(format string, args ...any) {
	fmt.Fprintf(r.Stderr, "glcli: "+format+"\n", args...)
}

// Print writes v on stdout as pruned JSON, with the pending warnings and
// hints. The top-level keys named in lists are always present: an empty
// list is printed as [] instead of being pruned, so that `jq '.items[]'`
// sees "nothing" rather than a missing field.
func (r *Runtime) Print(v any, lists ...string) error {
	compact, err := output.MarshalCompact(v)
	if err != nil {
		return err
	}
	r.mu.Lock()
	warnings, hints := r.warnings, r.hints
	r.warnings, r.hints = nil, nil
	r.mu.Unlock()
	extra := map[string]any{}
	present := topLevelKeys(compact)
	for _, k := range lists {
		if !present[k] {
			extra[k] = []any{}
		}
	}
	if len(warnings) > 0 {
		extra["warnings"] = warnings
	}
	if len(hints) > 0 {
		extra["hints"] = hints
	}
	out := compact
	for _, k := range append(lists, "warnings", "hints") {
		val, ok := extra[k]
		if !ok {
			continue
		}
		delete(extra, k)
		b, _ := json.Marshal(val)
		name, _ := json.Marshal(k)
		sep := ","
		if bytes.Equal(out, []byte("{}")) {
			sep = ""
		}
		out = append(append(out[:len(out)-1:len(out)-1], []byte(sep+string(name)+":"+string(b))...), '}')
	}
	if output.Indented(r.Stdout) {
		if out, err = output.Indent(out); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(r.Stdout, "%s\n", out)
	return err
}

func topLevelKeys(data []byte) map[string]bool {
	keys := map[string]bool{}
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) == nil {
		for k := range m {
			keys[k] = true
		}
	}
	return keys
}

// wakeStep bounds each sleep: timers stand still while a Mac sleeps, so
// the wall clock is read again at least this often.
var wakeStep = 5 * time.Second

// Poll calls check every interval until it returns done, until timeout
// (0: none) has passed, or until ctx ends. Transient errors (network down,
// rate limit, GitLab failing) are retried, with one note on stderr; other
// errors end the poll.
func (r *Runtime) Poll(ctx context.Context, interval, timeout time.Duration, check func() (done bool, err error)) (timedOut bool, err error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = r.Now().Round(0).Add(timeout)
	}
	failing := false
	for {
		done, err := check()
		switch {
		case err == nil:
			if failing {
				r.Notef("GitLab answers again")
				failing = false
			}
			if done {
				return false, nil
			}
		case ctx.Err() != nil:
			return false, ctx.Err()
		case !gitlab.IsTransient(err):
			return false, err
		case !failing:
			r.Notef("cannot reach GitLab, trying again every %s: %v", interval, err)
			failing = true
		}
		pause := interval
		if !deadline.IsZero() {
			left := deadline.Sub(r.Now().Round(0))
			if left <= 0 {
				return true, nil
			}
			pause = min(pause, left)
		}
		if err := r.sleepUntil(ctx, r.Now().Round(0).Add(pause)); err != nil {
			return false, err
		}
	}
}

func (r *Runtime) sleepUntil(ctx context.Context, t time.Time) error {
	for {
		left := t.Sub(r.Now().Round(0))
		if left <= 0 {
			return nil
		}
		timer := time.NewTimer(min(left, wakeStep))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// ParseSince reads a point in time: a duration back from now (30m, 12h,
// 2d), a day (2026-11-26, local midnight), a local date and time
// (2026-11-26T14:30) or an RFC 3339 timestamp.
func ParseSince(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err == nil && fmt.Sprintf("%dd", n) == s {
			return now.Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot read %q as a time: use 30m, 12h, 2d, 2026-11-26, 2026-11-26T14:30 or an RFC 3339 timestamp", s)
}
