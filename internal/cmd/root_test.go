package cmd

import (
	"bytes"
	"testing"
)

func TestUsageErrorsExit2(t *testing.T) {
	for _, args := range [][]string{
		{"nope"},
		{"mr", "get"},
		{"inbox", "extra"},
		{"mr", "wait", "a/b!1", "--on", "nope"},
		{"mr", "get", "a/b!1", "--no-such-flag"},
		{"mr", "get", "a/b!1", "--cursor", "garbage"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit %d, want 2 (stderr: %s)", args, code, stderr.String())
		}
	}
}
