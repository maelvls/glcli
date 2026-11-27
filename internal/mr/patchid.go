package mr

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/maelvls/glcli/internal/gitlab"
)

// PatchID hashes the changes of a diff like git patch-id: per file, its
// paths and its added and removed lines; hunk headers (line numbers) and
// context lines are ignored, so a rebase that keeps the changes keeps the
// hash. Only trailing whitespace is trimmed: an indentation fix is a
// change.
//
// known is false when the changes cannot be seen: GitLab left a file out
// for being too large, or a file changed without any line (a binary, a
// mode change), so two different versions of it would hash the same.
//
// GitLab's diffs start at the first hunk, without ---/+++ headers, so
// every line starting with - or + is a change, including "-- a comment".
func PatchID(diffs []gitlab.VersionDiff) (sum string, known bool) {
	files := make([]string, 0, len(diffs))
	for _, d := range diffs {
		if d.TooLarge || d.Collapsed {
			return "", false
		}
		var b strings.Builder
		b.WriteString(d.OldPath + "\x00" + d.NewPath + "\x00")
		if d.NewFile {
			b.WriteString("new\x00")
		}
		if d.DeletedFile {
			b.WriteString("deleted\x00")
		}
		changes := 0
		for _, line := range strings.Split(d.Diff, "\n") {
			if line == "" || (line[0] != '+' && line[0] != '-') {
				continue
			}
			b.WriteString(strings.TrimRight(line, " \t\r"))
			b.WriteByte('\n')
			changes++
		}
		pureRename := d.RenamedFile && strings.TrimSpace(d.Diff) == ""
		if changes == 0 && !pureRename {
			return "", false
		}
		files = append(files, b.String())
	}
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16], true
}
