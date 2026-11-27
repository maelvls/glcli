package cmd

import (
	"strings"

	"github.com/spf13/cobra"
)

// group makes cmd a group of subcommands: alone it prints its help, and
// anything else after it is a usage error that names the subcommands.
// Left to cobra, `glcli mr bogus` prints the help and exits 0.
func group(cmd *cobra.Command) *cobra.Command {
	run := func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		var names []string
		for _, sub := range cmd.Commands() {
			if sub.IsAvailableCommand() {
				names = append(names, sub.Name())
			}
		}
		path := cmd.CommandPath()
		return usagef("unknown command %q for %q: use %s (see %s --help)", args[0], path, oneOf(names), path)
	}
	cmd.Args = cobra.ArbitraryArgs
	cmd.RunE = run

	// cobra adds a "glcli mr [flags]" usage line to a command that runs:
	// hide RunE while the help of the group is printed, to keep it as it was.
	help := cmd.HelpFunc()
	cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		if c == cmd {
			c.RunE = nil
			defer func() { c.RunE = run }()
		}
		help(c, args)
	})
	return cmd
}

// oneOf writes names as "a, b or c".
func oneOf(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	last := len(names) - 1
	return strings.Join(names[:last], ", ") + " or " + names[last]
}
