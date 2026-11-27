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
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the embedded skills",
		Long:  `Prints {skills: [{name, description, files}]}.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			all, err := skills.List()
			if err != nil {
				return err
			}
			return rt.Print(struct {
				Skills []skills.Skill `json:"skills"`
			}{all}, "skills")
		},
	})

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
			if dir == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				dir = filepath.Join(home, ".claude", "skills")
			}
			abs, err := filepath.Abs(dir)
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
