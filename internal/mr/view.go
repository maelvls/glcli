package mr

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// View is the compact state of a merge request.
type View struct {
	Ref          string     `json:"ref"`
	URL          string     `json:"url"`
	Title        string     `json:"title"`
	State        string     `json:"state"`
	Draft        bool       `json:"draft,omitempty"`
	Author       string     `json:"author"`
	Reviewers    []string   `json:"reviewers,omitempty"`
	SourceBranch string     `json:"source_branch"`
	TargetBranch string     `json:"target_branch"`
	HeadSHA      string     `json:"head_sha"`
	PushedAt     *time.Time `json:"pushed_at,omitempty"`
	// MergeStatus is GitLab's detailed_merge_status (mergeable,
	// not_approved, ci_still_running, discussions_not_resolved, ...).
	MergeStatus  string        `json:"merge_status,omitempty"`
	HasConflicts bool          `json:"has_conflicts,omitempty"`
	AutoMerge    bool          `json:"auto_merge,omitempty"`
	MergedBy     string        `json:"merged_by,omitempty"`
	MergedAt     *time.Time    `json:"merged_at,omitempty"`
	MergeSHA     string        `json:"merge_sha,omitempty"`
	ClosedBy     string        `json:"closed_by,omitempty"`
	ClosedAt     *time.Time    `json:"closed_at,omitempty"`
	Approvals    ApprovalsView `json:"approvals"`
	Pipeline     *PipelineView `json:"pipeline,omitempty"`
	Threads      ThreadsView   `json:"threads"`
	LastByMe     *time.Time    `json:"last_comment_by_me_at,omitempty"`
	LastByOthers *NoteRef      `json:"last_comment_by_others,omitempty"`
	Description  string        `json:"description,omitempty"`
	UpdatedAt    time.Time     `json:"updated_at"`
	// Cursor captures this state: pass it to "mr wait --cursor" (or
	// "mr get --cursor") to learn what changed since.
	Cursor string `json:"cursor"`
}

// ApprovalsView summarises approvals.
type ApprovalsView struct {
	Required     int      `json:"required,omitempty"`
	Left         int      `json:"left,omitempty"`
	ApprovedBy   []string `json:"approved_by,omitempty"`
	ApprovedByMe bool     `json:"approved_by_me,omitempty"`
}

// PipelineView summarises the head pipeline.
type PipelineView struct {
	ID     int    `json:"id"`
	Status string `json:"status"`
	URL    string `json:"url"`
	SHA    string `json:"sha,omitempty"`
	// Stale is true when the pipeline did not run on the head commit (nor on
	// a merged-results commit of it): a push came after it.
	Stale bool `json:"stale,omitempty"`
}

// ThreadsView counts resolvable threads.
type ThreadsView struct {
	Resolvable int `json:"resolvable"`
	Unresolved int `json:"unresolved"`
	// UnresolvedMine are the unresolved threads I started.
	UnresolvedMine int `json:"unresolved_mine,omitempty"`
}

// NoteRef points at a note.
type NoteRef struct {
	Author string    `json:"author"`
	At     time.Time `json:"at"`
	URL    string    `json:"url"`
}

// ViewOptions tune the view.
type ViewOptions struct {
	Description  bool
	MaxBodyChars int
}

// NewView builds the compact state of s for the people p.
func NewView(s *Snapshot, p People, o ViewOptions) View {
	m := s.MR
	v := View{
		Ref:          s.Ref.String(),
		URL:          m.WebURL,
		Title:        m.Title,
		State:        m.State,
		Draft:        m.Draft,
		Author:       m.Author.Username,
		SourceBranch: m.SourceBranch,
		TargetBranch: m.TargetBranch,
		HeadSHA:      m.SHA,
		MergeStatus:  m.DetailedMergeStatus,
		HasConflicts: m.HasConflicts,
		AutoMerge:    m.MergeWhenPipelineOK,
		MergedAt:     m.MergedAt,
		UpdatedAt:    m.UpdatedAt,
	}
	switch m.State {
	case "merged":
		v.MergeSHA = firstNonEmpty(m.SquashCommitSHA, m.MergeCommitSHA, m.SHA)
		if u := m.Merger(); u != nil {
			v.MergedBy = u.Username
		}
	case "closed":
		// Only while closed: a reopened merge request may still carry
		// who closed it last.
		v.ClosedAt = m.ClosedAt
		if m.ClosedBy != nil {
			v.ClosedBy = m.ClosedBy.Username
		}
	}
	for _, r := range m.Reviewers {
		v.Reviewers = append(v.Reviewers, r.Username)
	}
	if len(s.Versions) > 0 {
		t := s.Versions[0].CreatedAt
		v.PushedAt = &t
	}
	v.Approvals = ApprovalsView{
		Required:   s.Approvals.ApprovalsRequired,
		Left:       s.Approvals.ApprovalsLeft,
		ApprovedBy: s.ApprovedBy(),
	}
	v.Approvals.ApprovedByMe = slices.Contains(v.Approvals.ApprovedBy, p.Me)
	if hp := m.HeadPipeline; hp != nil {
		v.Pipeline = &PipelineView{ID: hp.ID, Status: hp.Status, URL: hp.WebURL, SHA: hp.SHA, Stale: !s.PipelineIsHead}
	}
	for _, d := range s.Discussions {
		if len(d.Notes) == 0 {
			continue
		}
		first := d.Notes[0]
		if first.Resolvable {
			v.Threads.Resolvable++
			if !first.Resolved {
				v.Threads.Unresolved++
				if first.Author.Username == p.Me {
					v.Threads.UnresolvedMine++
				}
			}
		}
		for _, n := range d.Notes {
			if n.System {
				continue
			}
			switch {
			case n.Author.Username == p.Me:
				if v.LastByMe == nil || n.CreatedAt.After(*v.LastByMe) {
					t := n.CreatedAt
					v.LastByMe = &t
				}
			case p.Human(n.Author):
				if v.LastByOthers == nil || n.CreatedAt.After(v.LastByOthers.At) {
					v.LastByOthers = &NoteRef{Author: n.Author.Username, At: n.CreatedAt, URL: NoteURL(m.WebURL, n.ID)}
				}
			}
		}
	}
	if o.Description {
		v.Description = Truncate(m.Description, o.MaxBodyChars)
	}
	v.Cursor = NewCursor(s).Encode()
	return v
}

// NoteURL is the web URL of a note.
func NoteURL(mrURL string, noteID int) string {
	return mrURL + "#note_" + strconv.Itoa(noteID)
}

// Truncate cuts s to max runes (0: no limit), marking the cut.
func Truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "… [" + strconv.Itoa(len(r)-max) + " more chars]"
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
