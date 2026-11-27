package mr

import (
	"regexp"

	"github.com/maelvls/glcli/internal/gitlab"
)

// pushNote is the start of GitLab's system note for a push: "added 1
// commit" or "added 5 commits", then the list of commits.
var pushNote = regexp.MustCompile(`^added [0-9]+ commits?\b`)

// IsPushNote reports whether n is the system note GitLab writes when
// commits are pushed to a merge request. Many other system notes start
// with "added " (a label, a to-do item, a mention): they are not pushes,
// even when a label's name contains "commit".
func IsPushNote(n gitlab.Note) bool {
	return n.System && pushNote.MatchString(n.Body)
}
