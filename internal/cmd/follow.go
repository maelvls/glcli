package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/output"
)

// stream prints the lines of the --follow mode of mr wait and inbox wait:
// one compact JSON object per line, each with a "type". The lines that
// carry results ("events", "items") are printed by the commands with
// rt.PrintLine, with the warnings of their poll; stream prints the others
// ("warning", "end", "error"), which never carry warnings or hints.
// Every line carries what to resume from: "cursor" for mr wait,
// "checked_at" for inbox wait.
type stream struct {
	rt *cli.Runtime
	// resume fills the cursor or checked_at to resume from.
	resume func(*streamLine)
	// down is true after the line saying that GitLab cannot be reached,
	// until GitLab answers again.
	down bool
}

// streamLine is a line that is not a result.
type streamLine struct {
	Type      string     `json:"type"`
	Reason    string     `json:"reason,omitempty"`
	Message   string     `json:"message,omitempty"`
	Error     string     `json:"error,omitempty"`
	Cursor    string     `json:"cursor,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

func (s *stream) print(l streamLine) {
	if s.resume != nil {
		s.resume(&l)
	}
	b, err := output.MarshalCompact(l)
	if err != nil {
		return
	}
	fmt.Fprintf(s.rt.Stdout, "%s\n", b)
}

// outage prints that GitLab cannot be reached: rt.PollNotify calls it
// once per outage, when it writes its note on stderr.
func (s *stream) outage(err error) {
	s.down = true
	s.print(streamLine{Type: "warning", Message: "cannot reach GitLab, trying again: " + err.Error()})
}

// answered is called when GitLab answers a poll: after an outage, it
// prints that GitLab answers again, before the results of that poll.
func (s *stream) answered() {
	if s.down {
		s.down = false
		s.print(streamLine{Type: "warning", Message: "GitLab answers again"})
	}
}

// end prints the last line of a stream that ends normally: merged,
// closed or timeout.
func (s *stream) end(reason string) {
	s.print(streamLine{Type: "end", Reason: reason})
}

// stop prints why the stream stopped when the command returns err: an
// end line "interrupted" when ctx was cancelled (by SIGINT or SIGTERM;
// the command still exits with 130), an error line otherwise (the error
// is still written on stderr, with the usual exit code).
func (s *stream) stop(ctx context.Context, err error) {
	if err == nil {
		return
	}
	if ctx.Err() != nil {
		s.end("interrupted")
		return
	}
	s.print(streamLine{Type: "error", Error: err.Error()})
}
