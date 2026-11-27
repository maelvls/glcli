package cmd

import (
	"strings"

	"github.com/spf13/cobra"
)

// catchUnknownCommands makes an unknown word after a group of commands
// (`glcli mr bogus`) a usage error that names the commands of the group.
// Left to cobra, the group prints its help and glcli exits 0. The word
// "help" still prints the help, as --help does.
//
// A group has no Run, and cobra answers anything it is given with its
// help func, called once the flags of the group are parsed: that is where
// the unknown word is seen. A help func returns nothing, so the error is
// kept, and the func returned here gives it once root has executed.
func catchUnknownCommands(root *cobra.Command) func() error {
	var unknown error
	help := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		words := cmd.Flags().Args()
		asked, _ := cmd.Flags().GetBool("help")
		if asked || cmd.Runnable() || len(words) == 0 || words[0] == "help" {
			help(cmd, args)
			return
		}
		var names []string
		for _, sub := range cmd.Commands() {
			if sub.IsAvailableCommand() {
				names = append(names, sub.Name())
			}
		}
		path := cmd.CommandPath()
		unknown = usagef("unknown command %q for %q: use %s (see %s --help)", words[0], path, oneOf(names), path)
	})
	return func() error { return unknown }
}

// oneOf writes names as "a, b or c".
func oneOf(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	last := len(names) - 1
	return strings.Join(names[:last], ", ") + " or " + names[last]
}
