package ref

import (
	"errors"
	"testing"
)

func TestParseMR(t *testing.T) {
	remote := func() (string, string, error) { return "gitlab.com", "group/from-remote", nil }
	noRemote := func() (string, string, error) { return "", "", errors.New("not a git checkout") }
	tests := []struct {
		in      string
		d       Defaults
		want    MR
		wantErr bool
	}{
		{in: "https://gitlab.com/a/b/c/-/merge_requests/12", want: MR{"gitlab.com", "a/b/c", 12}},
		{in: "https://gitlab.com/a/b/-/merge_requests/12/diffs#note_99", want: MR{"gitlab.com", "a/b", 12}},
		{in: "https://code.pan.run/x/y/-/merge_requests/3?tab=pipelines", want: MR{"code.pan.run", "x/y", 3}},
		{in: "a/b!7", d: Defaults{Host: "gitlab.com"}, want: MR{"gitlab.com", "a/b", 7}},
		{in: "a/b!7", d: Defaults{Host: "gitlab.com", HostFlag: "code.pan.run"}, want: MR{"code.pan.run", "a/b", 7}},
		{in: "!7", d: Defaults{Host: "gitlab.com", Remote: remote}, want: MR{"gitlab.com", "group/from-remote", 7}},
		{in: "7", d: Defaults{Host: "gitlab.com", Remote: remote}, want: MR{"gitlab.com", "group/from-remote", 7}},
		{in: "!7", d: Defaults{Host: "gitlab.com", Repo: "https://code.pan.run/p/q", Remote: noRemote}, want: MR{"code.pan.run", "p/q", 7}},
		{in: "!7", d: Defaults{Host: "gitlab.com", Remote: noRemote}, wantErr: true},
		{in: "https://gitlab.com/a/b/-/pipelines/5", wantErr: true},
		{in: "a/b!x", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseMR(tt.in, tt.d)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseMR(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseMR(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestParsePipeline(t *testing.T) {
	p, ok, err := ParsePipeline("https://gitlab.com/a/b/-/pipelines/123")
	if err != nil || !ok || p != (Pipeline{"gitlab.com", "a/b", 123}) {
		t.Fatalf("got %+v %v %v", p, ok, err)
	}
	if _, ok, _ := ParsePipeline("a/b!1"); ok {
		t.Fatal("a/b!1 is not a pipeline URL")
	}
}

func TestParseRemote(t *testing.T) {
	tests := []struct{ in, host, project string }{
		{"git@gitlab.com:venafi/vaas/x.git", "gitlab.com", "venafi/vaas/x"},
		{"https://gitlab.com/venafi/vaas/x.git", "gitlab.com", "venafi/vaas/x"},
		{"https://gitlab.com/venafi/vaas/x", "gitlab.com", "venafi/vaas/x"},
		{"ssh://git@code.pan.run:2222/a/b.git", "code.pan.run", "a/b"},
	}
	for _, tt := range tests {
		h, p, ok := ParseRemote(tt.in)
		if !ok || h != tt.host || p != tt.project {
			t.Errorf("ParseRemote(%q) = %q %q %v", tt.in, h, p, ok)
		}
	}
	if _, _, ok := ParseRemote("not a remote"); ok {
		t.Error("expected failure")
	}
}
