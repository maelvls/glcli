package mr

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"github.com/maelvls/glcli/internal/gitlab"
)

// PatchID hashes a diff as the compare API gives it, to tell whether two
// diffs are the same changes when GitLab's own patch IDs cannot be used.
// Despite its name it is not git patch-id, and stricter: per file, it
// hashes the paths, the modes, whether the file is new, deleted or
// renamed, and the diff byte for byte, with its hunk headers (line
// numbers, heading), its context lines, its whitespace, its line endings
// and its "\ No newline at end of file" markers. Two diffs hash the same
// only when they are identical: a change never passes for a rebase.
//
// The price is that some rebases read as changes: git patch-id leaves
// the line numbers out, so it keeps its ID when the target branch added
// lines above a hunk, in the same file; this hash does not. Like git
// patch-id, it changes when the target branch changed the context lines
// of a hunk.
//
// known is false when the changes cannot be seen: GitLab left a diff out
// for being too large or collapsed, or a file has no changed line and is
// neither a pure rename nor a mode change (a binary file, whose diff is
// "Binary files a/x and b/x differ" whatever changed in it).
func PatchID(diffs []gitlab.VersionDiff) (sum string, known bool) {
	files := make([]string, 0, len(diffs))
	for _, d := range diffs {
		if d.TooLarge || d.Collapsed || !visible(d) {
			return "", false
		}
		var b strings.Builder
		flags := ""
		for _, f := range []struct {
			set  bool
			name string
		}{{d.NewFile, "new"}, {d.DeletedFile, "deleted"}, {d.RenamedFile, "renamed"}} {
			if f.set {
				flags += f.name + " "
			}
		}
		// Each field with its length: no two files can spell the same.
		for _, field := range []string{d.OldPath, d.NewPath, d.AMode, d.BMode, flags, d.Diff} {
			b.WriteString(strconv.Itoa(len(field)))
			b.WriteByte(':')
			b.WriteString(field)
		}
		files = append(files, b.String())
	}
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(strconv.Itoa(len(f)) + ":" + f))
	}
	return hex.EncodeToString(h.Sum(nil))[:16], true
}

// visible reports whether the diff of a file shows what changed in it:
// it has added or removed lines, or it is empty for a file that was only
// renamed or whose mode only changed. GitLab's diffs start at the first
// hunk, without ---/+++ headers, so every line starting with - or + is a
// change, including "-- a comment".
func visible(d gitlab.VersionDiff) bool {
	for _, line := range strings.Split(d.Diff, "\n") {
		if line != "" && (line[0] == '+' || line[0] == '-') {
			return true
		}
	}
	if strings.TrimSpace(d.Diff) != "" {
		return false // a notice, not a diff: "Binary files ... differ"
	}
	modeChange := d.AMode != "" && d.BMode != "" && d.AMode != d.BMode && !d.NewFile && !d.DeletedFile
	return d.RenamedFile || modeChange
}
