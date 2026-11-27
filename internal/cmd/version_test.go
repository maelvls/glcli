package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/output"
)

// A test binary has no VCS settings and no module version: only the Go
// version is sure to be there.
func TestVersionPrintsOneLineOfJSON(t *testing.T) {
	var outputs []string
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("%v: exit %d (stderr: %s)", args, code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("%v: stderr %q, want nothing", args, stderr.String())
		}
		out := stdout.String()
		if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
			t.Fatalf("%v: stdout %q, want one line", args, out)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%v: stdout %q is not JSON: %v", args, out, err)
		}
		if s, _ := got["go"].(string); s == "" {
			t.Errorf("%v: no go version in %s", args, out)
		}
		outputs = append(outputs, out)
	}
	if outputs[0] != outputs[1] {
		t.Errorf("version printed %q, --version %q", outputs[0], outputs[1])
	}
}

func TestVersionUsageErrorsExit2(t *testing.T) {
	for _, args := range [][]string{
		{"version", "extra"},
		{"version", "--no-such-flag"},
		{"mr", "--version"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit %d, want 2 (stderr: %s)", args, code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("%v: stdout %q, want nothing", args, stdout.String())
		}
	}
}

// The version is what an agent reports when glcli does not work: it must
// come out without glab's config, a token or a git checkout.
func TestVersionReadsNoConfig(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"--host", "gitlab.example.com", "version"}} {
		var stdout, stderr bytes.Buffer
		rt := cli.New(&stdout, &stderr)
		rt.LoadConfig = func() (*config.Config, error) {
			t.Errorf("%v: read glab's config", args)
			return nil, errors.New("no config")
		}
		rt.Remote = func() (string, string, error) {
			t.Errorf("%v: looked for a git remote", args)
			return "", "", errors.New("no remote")
		}
		if code := run(rt, args); code != 0 || stdout.Len() == 0 {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestVersionOf(t *testing.T) {
	checkout := func(modified string) []debug.BuildSetting {
		return []debug.BuildSetting{
			{Key: "-buildmode", Value: "exe"},
			{Key: "vcs", Value: "git"},
			{Key: "vcs.revision", Value: "558d978d4c1e0d2c7a8b9f3e5d6c7b8a9f0e1d2c"},
			{Key: "vcs.time", Value: "2026-11-27T15:52:11Z"},
			{Key: "vcs.modified", Value: modified},
		}
	}
	for _, tc := range []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{
			// The module cache is not a checkout: no VCS settings.
			name: "go install module@version",
			info: &debug.BuildInfo{GoVersion: "go1.27.1", Main: debug.Module{Path: "github.com/maelvls/glcli", Version: "v0.3.1"}},
			want: `{"version":"v0.3.1","go":"go1.27.1"}`,
		},
		{
			name: "clean checkout of a tag",
			info: &debug.BuildInfo{GoVersion: "go1.27.1", Main: debug.Module{Version: "v0.3.1"}, Settings: checkout("false")},
			want: `{"version":"v0.3.1","commit":"558d978d4c1e0d2c7a8b9f3e5d6c7b8a9f0e1d2c","commit_time":"2026-11-27T15:52:11Z","go":"go1.27.1"}`,
		},
		{
			name: "checkout with uncommitted changes",
			info: &debug.BuildInfo{GoVersion: "go1.27.1", Main: debug.Module{Version: "v0.3.2-0.20261127155211-558d978d4c1e+dirty"}, Settings: checkout("true")},
			want: `{"version":"v0.3.2-0.20261127155211-558d978d4c1e+dirty","commit":"558d978d4c1e0d2c7a8b9f3e5d6c7b8a9f0e1d2c","commit_time":"2026-11-27T15:52:11Z","modified":true,"go":"go1.27.1"}`,
		},
		{
			// Go's own word for "no version" is printed as it is.
			name: "no VCS information",
			info: &debug.BuildInfo{GoVersion: "go1.27.1", Main: debug.Module{Version: "(devel)"}},
			want: `{"version":"(devel)","go":"go1.27.1"}`,
		},
		{
			name: "test binary",
			info: &debug.BuildInfo{GoVersion: "go1.27.1"},
			want: `{"go":"go1.27.1"}`,
		},
		{
			name: "no build information",
			info: nil,
			want: `{}`,
		},
	} {
		got, err := output.MarshalCompact(versionOf(tc.info))
		if err != nil || string(got) != tc.want {
			t.Errorf("%s:\n got %s, %v\nwant %s", tc.name, got, err, tc.want)
		}
	}
}
