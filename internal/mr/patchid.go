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
// known is false when the changes cannot be seen: see visible.
func PatchID(diffs []gitlab.VersionDiff) (sum string, known bool) {
	files := make([]string, 0, len(diffs))
	for _, d := range diffs {
		if !visible(d) {
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

// visible reports whether the diff of a file shows what changed in it.
//
// It does when it has added or removed lines. GitLab's diffs start at
// the first hunk, without ---/+++ headers, so every line starting with -
// or + is a change, including "-- a comment".
//
// It does not when GitLab says it left the diff out (too_large,
// collapsed), nor when it is a notice: a binary file reads "Binary files
// a/x and b/x differ" whatever changed in it.
//
// An empty diff is that of a file only renamed, or whose mode only
// changed, but also what is left of a diff GitLab left out. Only GitLab
// 18.4 or newer tells the two apart, with too_large and collapsed, both
// false. Before, an empty diff shows nothing, whatever the rename and
// the modes say: a file renamed and edited, whose diff was left out for
// its size, looks the same after any further edit.
func visible(d gitlab.VersionDiff) bool {
	if d.TooLarge != nil && *d.TooLarge || d.Collapsed != nil && *d.Collapsed {
		return false
	}
	for _, line := range strings.Split(d.Diff, "\n") {
		if line != "" && (line[0] == '+' || line[0] == '-') {
			return true
		}
	}
	if strings.TrimSpace(d.Diff) != "" {
		return false // a notice, not a diff
	}
	if d.TooLarge == nil || d.Collapsed == nil {
		return false // GitLab before 18.4: maybe a diff left out
	}
	modeChange := d.AMode != "" && d.BMode != "" && d.AMode != d.BMode && !d.NewFile && !d.DeletedFile
	return d.RenamedFile || modeChange
}
