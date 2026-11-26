package mr

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
)

// Cursor is what an agent saw of a merge request, small enough to pass on
// a command line. Events are computed against it, so nothing that happens
// between two commands is lost.
type Cursor struct {
	SHA     string `json:"s"`
	Version int    `json:"v,omitempty"`
	// Base is the base commit of the head version: with SHA, it gives
	// the changes of the merge request, to tell a rebase from a change.
	Base  string `json:"b,omitempty"`
	State string `json:"st"`
	// MaxNote is the highest note ID seen: note IDs only grow, so notes
	// above it are new.
	MaxNote int `json:"n,omitempty"`
	// Unresolved are the first 12 characters of the IDs of the
	// unresolved threads.
	Unresolved []string `json:"u,omitempty"`
	Approvers  []string `json:"a,omitempty"`
	// Pipeline is "id:status" of the head pipeline.
	Pipeline string `json:"p,omitempty"`
}

const idPrefix = 12

// NewCursor captures s.
func NewCursor(s *Snapshot) Cursor {
	c := Cursor{SHA: s.MR.SHA, State: s.MR.State, Approvers: s.ApprovedBy()}
	if len(s.Versions) > 0 {
		c.Version = s.Versions[0].ID
		c.Base = s.Versions[0].BaseCommitSHA
	}
	for _, d := range s.Discussions {
		for _, n := range d.Notes {
			c.MaxNote = max(c.MaxNote, n.ID)
		}
		if len(d.Notes) > 0 && d.Notes[0].Resolvable && !d.Notes[0].Resolved {
			c.Unresolved = append(c.Unresolved, shortID(d.ID))
		}
	}
	slices.Sort(c.Unresolved)
	slices.Sort(c.Approvers)
	if hp := s.MR.HeadPipeline; hp != nil {
		c.Pipeline = strconv.Itoa(hp.ID) + ":" + hp.Status
	}
	return c
}

func shortID(id string) string {
	if len(id) > idPrefix {
		return id[:idPrefix]
	}
	return id
}

// Encode renders the cursor as an opaque URL-safe string.
func (c Cursor) Encode() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses what Encode produced.
func DecodeCursor(s string) (Cursor, error) {
	var c Cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	if err != nil || c.SHA == "" {
		return Cursor{}, fmt.Errorf("invalid --cursor: pass the \"cursor\" printed by mr get or mr wait")
	}
	return c, nil
}
