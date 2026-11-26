// Command glcli is a GitLab CLI for AI agents.
package main

import (
	"os"

	"github.com/maelvls/glcli/internal/cmd"
)

func main() {
	os.Exit(cmd.Run(os.Args[1:], os.Stdout, os.Stderr))
}
