package mr

import (
	"time"
)

// ThreadView is one discussion, compacted.
type ThreadView struct {
	ID         string     `json:"id"`
	Resolvable bool       `json:"resolvable,omitempty"`
	Resolved   bool       `json:"resolved,omitempty"`
	ResolvedBy string     `json:"resolved_by,omitempty"`
	File       string     `json:"file,omitempty"`
	Line       int        `json:"line,omitempty"`
	StartedBy  string     `json:"started_by"`
	URL        string     `json:"url"`
	Notes      []NoteView `json:"notes"`
}

// NoteView is one note of a thread.
type NoteView struct {
	ID     int       `json:"id"`
	Author string    `json:"author"`
	At     time.Time `json:"at"`
	System bool      `json:"system,omitempty"`
	Body   string    `json:"body"`
}

// ThreadFilter selects threads.
type ThreadFilter struct {
	Unresolved bool
	// Mine keeps the threads I started or replied to.
	Mine          bool
	IncludeSystem bool
	// AfterNote keeps the threads with a note above this ID, and only
	// those notes plus the first one (for context).
	AfterNote    int
	MaxBodyChars int
}

// Threads lists the discussions of s that pass f.
func Threads(s *Snapshot, p People, f ThreadFilter) []ThreadView {
	var out []ThreadView
	for _, d := range s.Discussions {
		if len(d.Notes) == 0 {
			continue
		}
		first := d.Notes[0]
		if first.System && !f.IncludeSystem {
			continue
		}
		if f.Unresolved && (!first.Resolvable || first.Resolved) {
			continue
		}
		mine, recent := false, false
		for _, n := range d.Notes {
			if n.Author.Username == p.Me && !n.System {
				mine = true
			}
			if n.ID > f.AfterNote {
				recent = true
			}
		}
		if f.Mine && !mine {
			continue
		}
		if f.AfterNote > 0 && !recent {
			continue
		}
		t := ThreadView{
			ID:         d.ID,
			Resolvable: first.Resolvable,
			Resolved:   first.Resolved,
			StartedBy:  first.Author.Username,
			URL:        NoteURL(s.MR.WebURL, first.ID),
		}
		if first.ResolvedBy != nil {
			t.ResolvedBy = first.ResolvedBy.Username
		}
		t.File, t.Line = position(first.Position)
		for i, n := range d.Notes {
			if n.System && !f.IncludeSystem {
				continue
			}
			if f.AfterNote > 0 && i > 0 && n.ID <= f.AfterNote {
				continue
			}
			t.Notes = append(t.Notes, NoteView{
				ID:     n.ID,
				Author: n.Author.Username,
				At:     n.CreatedAt,
				System: n.System,
				Body:   Truncate(n.Body, f.MaxBodyChars),
			})
		}
		out = append(out, t)
	}
	return out
}
