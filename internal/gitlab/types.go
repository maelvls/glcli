package gitlab

import (
	"regexp"
	"time"
)

// User is the user object GitLab embeds in most answers.
type User struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

var botUsername = regexp.MustCompile(`^(project|group)_\d+_bot(_[0-9a-z]+)?$|(^|[-_])bot$|^ghost$|^gitlab-bot$`)

// IsBot reports whether username looks like a machine: project and group
// access tokens (project_123_bot_abc), "*-bot" accounts, and the ghost user.
func IsBot(username string) bool {
	return botUsername.MatchString(username)
}

// PipelineRef is the pipeline summary embedded in merge requests.
type PipelineRef struct {
	ID        int       `json:"id"`
	ProjectID int       `json:"project_id"`
	SHA       string    `json:"sha"`
	Ref       string    `json:"ref"`
	Status    string    `json:"status"`
	Source    string    `json:"source"`
	WebURL    string    `json:"web_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MergeRequest is GET /projects/:id/merge_requests/:iid (and the items of
// the list endpoints, which lack head_pipeline).
type MergeRequest struct {
	ID                    int                   `json:"id"`
	IID                   int                   `json:"iid"`
	ProjectID             int                   `json:"project_id"`
	Title                 string                `json:"title"`
	Description           string                `json:"description"`
	State                 string                `json:"state"`
	Draft                 bool                  `json:"draft"`
	Author                User                  `json:"author"`
	Reviewers             []User                `json:"reviewers"`
	Assignees             []User                `json:"assignees"`
	SourceBranch          string                `json:"source_branch"`
	TargetBranch          string                `json:"target_branch"`
	SHA                   string                `json:"sha"`
	MergeCommitSHA        string                `json:"merge_commit_sha"`
	SquashCommitSHA       string                `json:"squash_commit_sha"`
	DetailedMergeStatus   string                `json:"detailed_merge_status"`
	HasConflicts          bool                  `json:"has_conflicts"`
	MergeWhenPipelineOK   bool                  `json:"merge_when_pipeline_succeeds"`
	HeadPipeline          *PipelineRef          `json:"head_pipeline"`
	WebURL                string                `json:"web_url"`
	References            struct{ Full string } `json:"references"`
	CreatedAt             time.Time             `json:"created_at"`
	UpdatedAt             time.Time             `json:"updated_at"`
	MergedAt              *time.Time            `json:"merged_at"`
	ClosedAt              *time.Time            `json:"closed_at"`
	MergeUser             *User                 `json:"merge_user"`
	MergedBy              *User                 `json:"merged_by"`
	ClosedBy              *User                 `json:"closed_by"`
	BlockingDiscussionsOK bool                  `json:"blocking_discussions_resolved"`
}

// Merger is who merged m: merge_user, or merged_by (deprecated since GitLab
// 14.7) when the instance does not send merge_user. On a merge request that
// is still open, it is who set auto-merge. Nil when GitLab names nobody.
func (m *MergeRequest) Merger() *User {
	if m.MergeUser != nil {
		return m.MergeUser
	}
	return m.MergedBy
}

// Approvals is GET /projects/:id/merge_requests/:iid/approvals.
type Approvals struct {
	Approved          bool `json:"approved"`
	ApprovalsRequired int  `json:"approvals_required"`
	ApprovalsLeft     int  `json:"approvals_left"`
	ApprovedBy        []struct {
		User User `json:"user"`
	} `json:"approved_by"`
}

// Position is where a diff note sits.
type Position struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	OldLine *int   `json:"old_line"`
	NewLine *int   `json:"new_line"`
}

// Note is one comment, or a system note ("added 1 commit").
type Note struct {
	ID         int       `json:"id"`
	Type       string    `json:"type"`
	Body       string    `json:"body"`
	Author     User      `json:"author"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	System     bool      `json:"system"`
	Resolvable bool      `json:"resolvable"`
	Resolved   bool      `json:"resolved"`
	ResolvedBy *User     `json:"resolved_by"`
	Position   *Position `json:"position"`
}

// Discussion is a thread of notes.
type Discussion struct {
	ID             string `json:"id"`
	IndividualNote bool   `json:"individual_note"`
	Notes          []Note `json:"notes"`
}

// Version is one diff version of a merge request (one per push).
type Version struct {
	ID             int       `json:"id"`
	HeadCommitSHA  string    `json:"head_commit_sha"`
	BaseCommitSHA  string    `json:"base_commit_sha"`
	StartCommitSHA string    `json:"start_commit_sha"`
	CreatedAt      time.Time `json:"created_at"`
	State          string    `json:"state"`
	// PatchIDSHA is GitLab's git patch-id of base..head: a rebase that
	// keeps the changes keeps it. It can be empty (not computed).
	PatchIDSHA string `json:"patch_id_sha"`
}

// VersionDiff is one file of a version.
type VersionDiff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	Diff        string `json:"diff"`
	NewFile     bool   `json:"new_file"`
	RenamedFile bool   `json:"renamed_file"`
	DeletedFile bool   `json:"deleted_file"`
	TooLarge    bool   `json:"too_large"`
	Collapsed   bool   `json:"collapsed"`
}

// VersionDetail is GET .../versions/:id.
type VersionDetail struct {
	Version
	Diffs []VersionDiff `json:"diffs"`
}

// Commit is a commit as listed by compare and MR commits.
type Commit struct {
	ID         string    `json:"id"`
	ShortID    string    `json:"short_id"`
	Title      string    `json:"title"`
	AuthorName string    `json:"author_name"`
	CreatedAt  time.Time `json:"created_at"`
	WebURL     string    `json:"web_url"`
}

// Pipeline is GET /projects/:id/pipelines/:pid.
type Pipeline struct {
	PipelineRef
	FinishedAt *time.Time `json:"finished_at"`
	Duration   *float64   `json:"duration"`
}

// Job is one job of a pipeline.
type Job struct {
	ID            int     `json:"id"`
	Name          string  `json:"name"`
	Stage         string  `json:"stage"`
	Status        string  `json:"status"`
	WebURL        string  `json:"web_url"`
	FailureReason string  `json:"failure_reason"`
	AllowFailure  bool    `json:"allow_failure"`
	Duration      float64 `json:"duration"`
}

// Bridge is a trigger job: it starts a downstream pipeline.
type Bridge struct {
	ID                 int          `json:"id"`
	Name               string       `json:"name"`
	Stage              string       `json:"stage"`
	Status             string       `json:"status"`
	WebURL             string       `json:"web_url"`
	AllowFailure       bool         `json:"allow_failure"`
	DownstreamPipeline *PipelineRef `json:"downstream_pipeline"`
}

// Todo is one item of the user's to-do list.
type Todo struct {
	ID         int    `json:"id"`
	ActionName string `json:"action_name"`
	TargetType string `json:"target_type"`
	TargetURL  string `json:"target_url"`
	Body       string `json:"body"`
	State      string `json:"state"`
	Author     User   `json:"author"`
	Project    struct {
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	Target struct {
		IID    int    `json:"iid"`
		Title  string `json:"title"`
		State  string `json:"state"`
		WebURL string `json:"web_url"`
		Draft  bool   `json:"draft"`
	} `json:"target"`
	CreatedAt time.Time `json:"created_at"`
}
