package mr

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/maelvls/glcli/internal/gitlab"
)

// PatchID hashes the changes of a diff version like git patch-id: per file,
// its paths and its added and removed lines with whitespace removed; hunk
// headers (line numbers) and context lines are ignored, so a rebase that
// keeps the changes keeps the hash. known is false when GitLab left a file
// out for being too large.
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
		for _, line := range strings.Split(d.Diff, "\n") {
			if line == "" || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
				continue
			}
			if line[0] != '+' && line[0] != '-' {
				continue
			}
			b.WriteByte(line[0])
			b.WriteString(strings.Join(strings.Fields(line[1:]), ""))
			b.WriteByte('\n')
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
