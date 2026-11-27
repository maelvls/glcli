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
	// MR is "project_id!iid": a cursor is only valid for its merge request.
	MR      string `json:"m,omitempty"`
	SHA     string `json:"s"`
	Version int    `json:"v,omitempty"`
	// Base is the base commit of the head version: with SHA, it gives
	// the changes of the merge request, to tell a rebase from a change.
	Base string `json:"b,omitempty"`
	// Patch is GitLab's patch ID (patch_id_sha) of the head version: the
	// same one after a push means the same changes, without asking GitLab
	// to compare anything. Empty when GitLab had not computed it, and in
	// the cursors printed by older versions of glcli.
	Patch string `json:"pi,omitempty"`
	// State is opened, closed or merged, never locked: see settledState.
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
	c := Cursor{MR: s.ID(), SHA: s.MR.SHA, State: settledState(s.MR.State), Approvers: s.ApprovedBy()}
	// The base and the patch ID only go with the head when all three come
	// from the same push.
	if len(s.Versions) > 0 && !s.Unsettled {
		c.Version = s.Versions[0].ID
		c.Base = s.Versions[0].BaseCommitSHA
		c.Patch = s.Versions[0].PatchIDSHA
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

// settledState is the state as cursors and events see it. GitLab's
// "locked" is short-lived and transitional: an opened merge request
// whose merge has started, and which then becomes merged, or opened
// again when the merge fails. It counts as opened: a poll that catches
// it reports nothing, the cursor printed then says "opened", and the
// merge is reported once, as opened to merged.
func settledState(state string) string {
	if state == "locked" {
		return "opened"
	}
	return state
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

// Check reports a cursor taken on another merge request.
func (c Cursor) Check(s *Snapshot) error {
	if c.MR != "" && c.MR != s.ID() {
		return fmt.Errorf("--cursor belongs to another merge request (%s, this one is %s): pass the cursor printed for this one", c.MR, s.ID())
	}
	return nil
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
