package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// groups are the commands that only hold subcommands.
var groups = map[string][]string{
	"mr":       {"discussions", "get", "wait"},
	"pipeline": {"get", "wait"},
	"auth":     {"status", "test"},
	"skills":   {"install", "list"},
}

func TestGroupUnknownSubcommandExits2(t *testing.T) {
	for name, subs := range groups {
		for _, args := range [][]string{
			{name, "bogus"},
			{name, "--host", "gitlab.example.com", "bogus"},
			{name, "bogus", "a/b!1"},
		} {
			var stdout, stderr bytes.Buffer
			if code := Run(args, &stdout, &stderr); code != 2 {
				t.Errorf("%v: exit %d, want 2 (stderr: %s)", args, code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("%v: stdout is not empty: %s", args, stdout.String())
			}
			want := `glcli: unknown command "bogus" for "glcli ` + name + `": use `
			if !strings.HasPrefix(stderr.String(), want) {
				t.Errorf("%v: stderr is %q, want it to start with %q", args, stderr.String(), want)
			}
			for _, sub := range subs {
				if !strings.Contains(stderr.String(), sub) {
					t.Errorf("%v: stderr does not name the subcommand %q: %s", args, sub, stderr.String())
				}
			}
		}
	}
}

func TestGroupUnknownSubcommandMessage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	Run([]string{"mr", "list"}, &stdout, &stderr)
	want := `glcli: unknown command "list" for "glcli mr": use discussions, get or wait (see glcli mr --help)` + "\n"
	if stderr.String() != want {
		t.Errorf("stderr is %q, want %q", stderr.String(), want)
	}
}

func TestBareGroupPrintsHelp(t *testing.T) {
	for name := range groups {
		var bare, stderr bytes.Buffer
		if code := Run([]string{name}, &bare, &stderr); code != 0 {
			t.Errorf("%s: exit %d, want 0 (stderr: %s)", name, code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("%s: stderr is not empty: %s", name, stderr.String())
		}
		// The only usage line is the one of a group.
		usage := "Usage:\n  glcli " + name + " [command]\n\n"
		if !strings.Contains(bare.String(), usage) {
			t.Errorf("%s: the help does not have %q: %s", name, usage, bare.String())
		}

		var help bytes.Buffer
		if code := Run([]string{name, "--help"}, &help, &stderr); code != 0 {
			t.Errorf("%s --help: exit %d, want 0 (stderr: %s)", name, code, stderr.String())
		}
		if help.String() != bare.String() {
			t.Errorf("%s --help prints %q, want the same as %s alone: %q", name, help.String(), name, bare.String())
		}
	}
}

func TestOneOf(t *testing.T) {
	for want, names := range map[string][]string{
		"":             nil,
		"a":            {"a"},
		"a or b":       {"a", "b"},
		"a, b or c":    {"a", "b", "c"},
		"a, b, c or d": {"a", "b", "c", "d"},
	} {
		if got := oneOf(names); got != want {
			t.Errorf("oneOf(%v) = %q, want %q", names, got, want)
		}
	}
}
