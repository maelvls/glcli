package cmd

import (
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/output"
)

// buildVersion is what `glcli version` prints. What the go command did not
// record in the binary is absent.
type buildVersion struct {
	Version    string `json:"version,omitempty"`
	Commit     string `json:"commit,omitempty"`
	CommitTime string `json:"commit_time,omitempty"`
	Modified   bool   `json:"modified,omitempty"`
	Go         string `json:"go,omitempty"`
}

// versionOf takes everything from what the go command records in every
// binary, so that no -ldflags are needed: the module version as Go gives
// it (a tag, a pseudo-version when the commit is not a tag, or "(devel)"),
// and the commit when the build was made from a checkout. info is nil
// when the binary has no build information.
func versionOf(info *debug.BuildInfo) buildVersion {
	if info == nil {
		return buildVersion{}
	}
	v := buildVersion{Version: info.Main.Version, Go: info.GoVersion}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			v.Commit = s.Value
		case "vcs.time":
			v.CommitTime = s.Value
		case "vcs.modified":
			v.Modified = s.Value == "true"
		}
	}
	return v
}

func readVersion() buildVersion {
	info, _ := debug.ReadBuildInfo()
	return versionOf(info)
}

// addVersion adds `glcli version` and `glcli --version`, which print the
// same thing. Neither reads glab's config nor calls GitLab: an agent runs
// them to report which glcli is installed, including when nothing else
// works.
func addVersion(root *cobra.Command, rt *cli.Runtime) {
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version of glcli (and its commit, for a build from a checkout)",
		Long: `Prints {version, commit, commit_time, modified, go}, as the go command
recorded them in the binary; what it did not record is absent.

version is the module version as Go gives it: v0.3.1 for the tag v0.3.1;
a pseudo-version ending in -<commit time>-<first 12 characters of the
commit> when the commit is not a tag (v0.0.0-... before the first tag,
the next patch version after one: v0.3.2-0.<commit time>-<commit> is a
commit after v0.3.1, not a v0.3.2); "(devel)" when Go recorded none. It
ends in +dirty when the checkout had changes or untracked files.

commit, commit_time and modified (only when true: the checkout had
changes or untracked files) are there only for a build made from a git
checkout, such as "make install". "go install ...@version" records no
commit: there, a pseudo-version is what names it.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return rt.Print(readVersion())
		},
	})

	// cobra answers --version itself, before any hook of the root command,
	// by printing root.Version through a template: the JSON is made here,
	// the way rt.Print makes it for rt.Stdout.
	line, err := output.Format(rt.Stdout, readVersion())
	if err != nil {
		return
	}
	root.Version = string(line)
	root.SetVersionTemplate("{{.Version}}\n")
	// Declared here because the flag cobra would add also takes -v.
	root.Flags().Bool("version", false, "Print the version of glcli (same as the version command)")
}
