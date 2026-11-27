package cmd

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/skills"
)

func newSkills(rt *cli.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "List and install the agent skills embedded in glcli",
		Long: `The agent skills that teach an agent (Claude Code and others that read
SKILL.md directories) how to use glcli are embedded in the binary, so
that installing glcli is enough to install them.`,
	}
	var listDir string
	list := &cobra.Command{
		Use:   "list",
		Short: "List the embedded skills, and whether the installed ones are the same",
		Long: `Prints {dir, skills: [{name, description, files, installed, symlink,
error}]}.

installed says how the skill of that name in --dir compares with the one
embedded in this glcli (same files, same bytes): absent, identical,
differs (it comes from another version of glcli, or was edited), or
unreadable, with the error. symlink is where the installed skill points
to when it is a symlink.

It only reads: nothing is written and no directory is created, whatever
--dir is. That makes it the way to check that the installed skill and
this glcli go together; "glcli skills install" is what writes.`,
		Example: `  glcli skills list
  glcli skills list --dir ./.claude/skills`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			abs, err := skillsDir(listDir)
			if err != nil {
				// The embedded skills can still be listed.
				rt.Warnf("Cannot tell whether the skills are installed: %v. Pass --dir.", err)
				all, err := skills.List()
				if err != nil {
					return err
				}
				return rt.Print(struct {
					Skills []skills.Skill `json:"skills"`
				}{all}, "skills")
			}
			all, err := skills.Compare(abs)
			if err != nil {
				return err
			}
			return rt.Print(struct {
				Dir    string         `json:"dir"`
				Skills []skills.Skill `json:"skills"`
			}{abs, all}, "skills")
		},
	}
	list.Flags().StringVar(&listDir, "dir", "", "Skills directory to compare with (default ~/.claude/skills)")
	cmd.AddCommand(list)

	var (
		dir       string
		overwrite bool
	)
	install := &cobra.Command{
		Use:   "install",
		Short: "Install the embedded skills into a skills directory (default ~/.claude/skills)",
		Long: `Write each embedded skill into --dir, one directory per skill (for
example ~/.claude/skills/glcli/SKILL.md).

A skill already there is left alone unless --overwrite, which replaces it
entirely (files no longer shipped go away). A skill that is a symlink, such
as one pointing at a checkout of glcli for development, is always left
alone: remove the symlink to install a copy instead.

Prints {dir, skills: [{name, path, status, reason, differs}]}, status
being installed, replaced or skipped. Run it again after upgrading glcli,
with --overwrite, to update the skills.

A skipped skill is compared with the one embedded in this glcli (same
files, same bytes): reason says whether it is identical, and differs is
true when it is not, which means that the installed skill comes from
another version of glcli or was edited. Nothing is written for that:
without --overwrite, a skill already installed is only read.`,
		Example: `  glcli skills install
  glcli skills install --overwrite
  glcli skills install --dir ./.claude/skills`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			abs, err := skillsDir(dir)
			if err != nil {
				return err
			}
			res, err := skills.Install(abs, overwrite)
			if err != nil {
				return err
			}
			for _, r := range res {
				// Not for a symlink: --overwrite leaves it alone.
				if r.Status == "skipped" && r.Differs && !r.Symlink {
					rt.Hintf("Some installed skills differ from this glcli's: pass --overwrite to replace them.")
					break
				}
			}
			return rt.Print(struct {
				Dir    string          `json:"dir"`
				Skills []skills.Result `json:"skills"`
			}{abs, res}, "skills")
		},
	}
	install.Flags().StringVar(&dir, "dir", "", "Skills directory to install into (default ~/.claude/skills)")
	install.Flags().BoolVar(&overwrite, "overwrite", false, "Replace skills that are already installed")
	cmd.AddCommand(install)
	return cmd
}

// skillsDir is --dir, or ~/.claude/skills.
func skillsDir(dir string) (string, error) {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".claude", "skills")
	}
	return filepath.Abs(dir)
}
