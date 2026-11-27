package cmd

import (
	"bytes"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/maelvls/glcli/internal/cli"
)

// groups walks the commands of glcli and returns those that only hold
// subcommands, each as the arguments that lead to it: none for glcli
// itself. A group added later is found without touching these tests.
func groups(t *testing.T) [][]string {
	t.Helper()
	root := newRoot(cli.New(io.Discard, io.Discard))
	// cobra adds these two when it executes.
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()

	var found [][]string
	var walk func(cmd *cobra.Command, path []string)
	walk = func(cmd *cobra.Command, path []string) {
		if cmd.HasSubCommands() && !cmd.Runnable() {
			found = append(found, path)
		}
		for _, sub := range cmd.Commands() {
			walk(sub, append(slices.Clone(path), sub.Name()))
		}
	}
	walk(root, nil)

	for _, want := range []string{"", "mr", "pipeline", "auth", "skills", "completion"} {
		if !slices.ContainsFunc(found, func(path []string) bool { return strings.Join(path, " ") == want }) {
			t.Fatalf("the walk did not find the group %q: %v", want, found)
		}
	}
	return found
}

// groupRun runs glcli with the arguments of a group followed by more.
func groupRun(group []string, more ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = Run(append(slices.Clone(group), more...), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestGroupUnknownCommandExits2(t *testing.T) {
	for _, group := range groups(t) {
		path := strings.Join(append([]string{"glcli"}, group...), " ")
		for _, more := range [][]string{
			{"bogus"},
			{"bogus", "a/b!1"},
			{"--host", "gitlab.example.com", "bogus"},
			{"bogus", "--host", "gitlab.example.com"},
		} {
			code, stdout, stderr := groupRun(group, more...)
			if code != 2 {
				t.Errorf("%s %v: exit %d, want 2 (stderr: %s)", path, more, code, stderr)
			}
			if stdout != "" {
				t.Errorf("%s %v: stdout is not empty: %s", path, more, stdout)
			}
			want := `glcli: unknown command "bogus" for "` + path + `"`
			if !strings.HasPrefix(stderr, want) {
				t.Errorf("%s %v: stderr is %q, want it to start with %q", path, more, stderr, want)
			}
		}
	}
}

func TestGroupUnknownCommandMessage(t *testing.T) {
	for want, args := range map[string][]string{
		`glcli: unknown command "list" for "glcli mr": use discussions, get or wait (see glcli mr --help)`:                       {"mr", "list"},
		`glcli: unknown command "bogus" for "glcli pipeline": use get or wait (see glcli pipeline --help)`:                       {"pipeline", "bogus"},
		`glcli: unknown command "bogus" for "glcli auth": use status or test (see glcli auth --help)`:                            {"auth", "bogus"},
		`glcli: unknown command "nope" for "glcli skills": use install or list (see glcli skills --help)`:                        {"skills", "nope"},
		`glcli: unknown command "bogus" for "glcli completion": use bash, fish, powershell or zsh (see glcli completion --help)`: {"completion", "bogus"},
	} {
		code, stdout, stderr := groupRun(args)
		if code != 2 || stdout != "" || stderr != want+"\n" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q, want exit 2, no stdout and stderr %q", args, code, stdout, stderr, want+"\n")
		}
	}
}

// After "--", cobra does not look for a command: the word is still not one
// that glcli can run.
func TestUnknownCommandAfterDoubleDashExits2(t *testing.T) {
	code, stdout, stderr := groupRun(nil, "--", "bogus")
	if code != 2 || stdout != "" || !strings.HasPrefix(stderr, `glcli: unknown command "bogus" for "glcli"`) {
		t.Errorf("exit %d, stdout %q, stderr %q, want exit 2, no stdout and an unknown command on stderr", code, stdout, stderr)
	}
}

func TestGroupHelp(t *testing.T) {
	for _, group := range groups(t) {
		path := strings.Join(append([]string{"glcli"}, group...), " ")

		code, bare, stderr := groupRun(group)
		if code != 0 || stderr != "" {
			t.Errorf("%s: exit %d, stderr %q, want exit 0 and no stderr", path, code, stderr)
		}
		// The only usage line is the one of a group.
		usage := "Usage:\n  " + path + " [command]\n\n"
		if !strings.Contains(bare, usage) {
			t.Errorf("%s: the help does not have %q: %s", path, usage, bare)
		}

		for _, args := range [][]string{
			append(slices.Clone(group), "--help"),
			append(slices.Clone(group), "-h"),
			append(slices.Clone(group), "help"),
			append([]string{"help"}, group...),
		} {
			code, stdout, stderr := groupRun(args)
			if code != 0 || stderr != "" {
				t.Errorf("glcli %v: exit %d, stderr %q, want exit 0 and no stderr", args, code, stderr)
			}
			if stdout != bare {
				t.Errorf("glcli %v prints %q, want the same as %s alone: %q", args, stdout, path, bare)
			}
		}
	}
}

// What asks for help wins over an unknown word.
func TestGroupHelpWithMoreWords(t *testing.T) {
	_, want, _ := groupRun([]string{"mr", "--help"})
	for _, args := range [][]string{
		{"mr", "bogus", "--help"},
		{"mr", "--help", "bogus"},
		{"mr", "bogus", "-h"},
		{"mr", "help", "get"},
		{"mr", "help", "bogus"},
	} {
		code, stdout, stderr := groupRun(args)
		if code != 0 || stderr != "" || stdout != want {
			t.Errorf("glcli %v: exit %d, stderr %q, stdout %q, want exit 0, no stderr and the help of mr", args, code, stderr, stdout)
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
