package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in   string
		want time.Time
	}{
		{"30m", now.Add(-30 * time.Minute)},
		{"2d", now.Add(-48 * time.Hour)},
		{"2026-11-26T10:00:00Z", time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)},
		{"2026-11-25", time.Date(2026, 11, 25, 0, 0, 0, 0, time.Local)},
	}
	for _, tt := range tests {
		got, err := ParseSince(tt.in, now)
		if err != nil || !got.Equal(tt.want) {
			t.Errorf("ParseSince(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	if _, err := ParseSince("yesterday", now); err == nil {
		t.Error("expected an error")
	}
}

func TestPrintKeepsListsAndAddsHints(t *testing.T) {
	var out bytes.Buffer
	rt := New(&out, &bytes.Buffer{})
	rt.Hintf("do %s", "this")
	rt.Warnf("careful")
	if err := rt.Print(struct {
		Items []int  `json:"items"`
		Name  string `json:"name"`
		Empty string `json:"empty"`
	}{Name: "x"}, "items"); err != nil {
		t.Fatal(err)
	}
	want := `{"name":"x","items":[],"warnings":["careful"],"hints":["do this"]}` + "\n"
	if out.String() != want {
		t.Errorf("got %s want %s", out.String(), want)
	}
	// Hints are consumed by the first Print.
	out.Reset()
	_ = rt.Print(struct{}{}, "items")
	if out.String() != `{"items":[]}`+"\n" {
		t.Errorf("second print: %s", out.String())
	}
}

type transient struct{}

func (transient) Error() string   { return "connection refused" }
func (transient) Timeout() bool   { return false }
func (transient) Temporary() bool { return true }

func TestPollRetriesTransientErrors(t *testing.T) {
	var stderr bytes.Buffer
	rt := New(&bytes.Buffer{}, &stderr)
	calls := 0
	timedOut, err := rt.Poll(context.Background(), 10*time.Millisecond, 0, func() (bool, error) {
		calls++
		switch calls {
		case 1, 2:
			return false, transient{}
		case 3:
			return false, nil
		}
		return true, nil
	})
	if err != nil || timedOut || calls != 4 {
		t.Fatalf("calls=%d timedOut=%v err=%v", calls, timedOut, err)
	}
	if n := strings.Count(stderr.String(), "cannot reach GitLab"); n != 1 {
		t.Errorf("want one note about the outage, got %d: %s", n, stderr.String())
	}
}

func TestPollStopsOnOtherErrors(t *testing.T) {
	rt := New(&bytes.Buffer{}, &bytes.Buffer{})
	boom := errors.New("boom")
	_, err := rt.Poll(context.Background(), time.Millisecond, 0, func() (bool, error) { return false, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

func TestPollTimeout(t *testing.T) {
	rt := New(&bytes.Buffer{}, &bytes.Buffer{})
	timedOut, err := rt.Poll(context.Background(), 10*time.Millisecond, 30*time.Millisecond, func() (bool, error) { return false, nil })
	if err != nil || !timedOut {
		t.Fatalf("timedOut=%v err=%v", timedOut, err)
	}
}
