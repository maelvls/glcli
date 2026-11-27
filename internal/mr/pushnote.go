package mr

import (
	"regexp"

	"github.com/maelvls/glcli/internal/gitlab"
)

// pushNote is the start of GitLab's system note for a push: "added 1
// commit" or "added 5 commits", then the list of commits. GitLab has no
// other wording for it (SystemNotes::CommitService#add_commits).
var pushNote = regexp.MustCompile(`^added [0-9]+ commits?\b`)

// IsPushNote reports whether n is the system note GitLab writes when
// commits are pushed to a merge request. Other system notes start with
// "added " and are not pushes: time spent ("added 1h of time spent at
// ..."), a time estimate ("added time estimate of 1h") and, on merge
// requests old enough to have them as notes, labels ("added ~12 label").
func IsPushNote(n gitlab.Note) bool {
	return n.System && pushNote.MatchString(n.Body)
}
