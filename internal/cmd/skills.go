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

Prints {dir, skills: [{name, path, status, reason}]}, status being
installed, replaced or skipped. Run it again after upgrading glcli, with
--overwrite, to update the skills.`,
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
				if r.Status == "skipped" && !overwrite {
					rt.Hintf("Some skills were already installed: pass --overwrite to replace them with this version of glcli's.")
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
	return group(cmd)
}
