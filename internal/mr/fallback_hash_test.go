package mr

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// file is a.go changed by diff, as the compare API gives it.
func file(diff string) gitlab.VersionDiff {
	return gitlab.VersionDiff{OldPath: "a.go", NewPath: "a.go", AMode: "100644", BMode: "100644", Diff: diff}
}

// Pairs of different changes that glcli's own hash used to call the same:
// it only read the added and removed lines, trimmed. None may pass for a
// rebase: either the hashes differ, or one is unknown.
func TestPatchIDTellsDifferentChangesApart(t *testing.T) {
	exec := file("@@ -1,3 +1,3 @@\n a\n-b\n+c\n d\n")
	exec.BMode = "100755"
	tests := []struct {
		name string
		a, b gitlab.VersionDiff
	}{
		{
			name: "the same added line, in func A or in func B, with other context lines",
			a:    file("@@ -10,6 +10,7 @@ func A() {\n \ta := 1\n \tb := 2\n \tc := 3\n+\tlog.Println(\"here\")\n \treturn a\n }\n \n"),
			b:    file("@@ -30,6 +30,7 @@ func B() {\n \tx := 1\n \ty := 2\n \tz := 3\n+\tlog.Println(\"here\")\n \treturn x\n }\n \n"),
		},
		{
			name: "the same added line, in func A or in func B, with the same context lines",
			a:    file("@@ -10,6 +10,7 @@ func A() {\n \ta := 1\n \tb := 2\n \tc := 3\n+\tlog.Println(\"here\")\n \treturn nil\n }\n \n"),
			b:    file("@@ -30,6 +30,7 @@ func B() {\n \ta := 1\n \tb := 2\n \tc := 3\n+\tlog.Println(\"here\")\n \treturn nil\n }\n \n"),
		},
		{
			name: "the same added line elsewhere in the same function, with the same context lines",
			a:    file("@@ -10,6 +10,7 @@ func A() {\n \ta := 1\n \tb := 2\n \tc := 3\n+\tlog.Println(\"here\")\n \treturn nil\n }\n \n"),
			b:    file("@@ -18,6 +18,7 @@ func A() {\n \ta := 1\n \tb := 2\n \tc := 3\n+\tlog.Println(\"here\")\n \treturn nil\n }\n \n"),
		},
		{
			name: "timeout 30 to 60, under staging or under production",
			a:    file("@@ -3,7 +3,7 @@ staging:\n   replicas: 2\n   cpu: 500m\n   memory: 1Gi\n-  timeout: 30\n+  timeout: 60\n   retries: 3\n   backoff: 2\n   log: info\n"),
			b:    file("@@ -12,7 +12,7 @@ production:\n   replicas: 2\n   cpu: 500m\n   memory: 1Gi\n-  timeout: 30\n+  timeout: 60\n   retries: 3\n   backoff: 2\n   log: info\n"),
		},
		{
			name: "chmod +x on a file that also changes",
			a:    file("@@ -1,3 +1,3 @@\n a\n-b\n+c\n d\n"),
			b:    exec,
		},
		{
			name: "trailing whitespace: a Markdown line break",
			a:    file("@@ -1,2 +1,2 @@\n title\n-old\n+first line\n"),
			b:    file("@@ -1,2 +1,2 @@\n title\n-old\n+first line  \n"),
		},
		{
			name: "CRLF or LF",
			a:    file("@@ -1,2 +1,2 @@\n title\n-old\n+new\n"),
			b:    file("@@ -1,2 +1,2 @@\n title\n-old\n+new\r\n"),
		},
		{
			name: "a newline added at the end of the file",
			a:    file("@@ -1,2 +1,2 @@\n title\n-old\n+new\n\\ No newline at end of file\n"),
			b:    file("@@ -1,2 +1,2 @@\n title\n-old\n+new\n"),
		},
		{
			name: "a tab or spaces",
			a:    file("@@ -1 +1 @@\n-old\n+\tnew\n"),
			b:    file("@@ -1 +1 @@\n-old\n+    new\n"),
		},
		{
			name: "another context line",
			a:    file("@@ -1,3 +1,3 @@\n if a {\n-old\n+new\n }\n"),
			b:    file("@@ -1,3 +1,3 @@\n if b {\n-old\n+new\n }\n"),
		},
		{
			name: "a new file or a changed one",
			a:    gitlab.VersionDiff{OldPath: "a.go", NewPath: "a.go", AMode: "0", BMode: "100644", NewFile: true, Diff: "@@ -0,0 +1 @@\n+new\n"},
			b:    gitlab.VersionDiff{OldPath: "a.go", NewPath: "a.go", AMode: "100644", BMode: "100644", Diff: "@@ -0,0 +1 @@\n+new\n"},
		},
		{
			name: "a rename, to one name or to another",
			a:    gitlab.VersionDiff{OldPath: "a.go", NewPath: "b.go", AMode: "100644", BMode: "100644", RenamedFile: true},
			b:    gitlab.VersionDiff{OldPath: "a.go", NewPath: "c.go", AMode: "100644", BMode: "100644", RenamedFile: true},
		},
		{
			name: "a mode change, to one mode or to another",
			a:    gitlab.VersionDiff{OldPath: "run", NewPath: "run", AMode: "100644", BMode: "100755"},
			b:    gitlab.VersionDiff{OldPath: "run", NewPath: "run", AMode: "100644", BMode: "120000"},
		},
		{
			name: "paths that spell the same when put end to end",
			a:    gitlab.VersionDiff{OldPath: "ab", NewPath: "c", RenamedFile: true},
			b:    gitlab.VersionDiff{OldPath: "a", NewPath: "bc", RenamedFile: true},
		},
	}
	for _, tt := range tests {
		a, aKnown := PatchID([]gitlab.VersionDiff{tt.a})
		b, bKnown := PatchID([]gitlab.VersionDiff{tt.b})
		if !aKnown || !bKnown {
			t.Errorf("%s: known = %v, %v; want both hashed", tt.name, aKnown, bKnown)
		}
		if a == b {
			t.Errorf("%s: both hash to %s", tt.name, a)
		}
		// And each one is the same as itself, whatever the order of the files.
		other := file("@@ -1 +1 @@\n-x\n+y\n")
		other.OldPath, other.NewPath = "z.go", "z.go"
		x, _ := PatchID([]gitlab.VersionDiff{tt.a, other})
		y, _ := PatchID([]gitlab.VersionDiff{other, tt.a})
		if x != y || x == a {
			t.Errorf("%s: with another file, %s and %s (alone: %s)", tt.name, x, y, a)
		}
	}
}

// What the hash cannot see is unknown, never "the same".
func TestPatchIDUnknown(t *testing.T) {
	tests := []struct {
		name  string
		d     gitlab.VersionDiff
		known bool
	}{
		{"a binary file", gitlab.VersionDiff{OldPath: "logo.png", NewPath: "logo.png", AMode: "100644", BMode: "100644", Diff: "Binary files a/logo.png and b/logo.png differ\n"}, false},
		{"a binary file whose mode changes too", gitlab.VersionDiff{OldPath: "run", NewPath: "run", AMode: "100644", BMode: "100755", Diff: "Binary files a/run and b/run differ\n"}, false},
		{"a binary file renamed and changed", gitlab.VersionDiff{OldPath: "a.png", NewPath: "b.png", RenamedFile: true, Diff: "Binary files a/a.png and b/b.png differ\n"}, false},
		{"a new binary file", gitlab.VersionDiff{OldPath: "a.png", NewPath: "a.png", AMode: "0", BMode: "100644", NewFile: true, Diff: "Binary files /dev/null and b/a.png differ\n"}, false},
		{"a file with no diff, no rename and no mode change", gitlab.VersionDiff{OldPath: "a", NewPath: "a", AMode: "100644", BMode: "100644"}, false},
		{"a file with no diff and no modes", gitlab.VersionDiff{OldPath: "a", NewPath: "a"}, false},
		{"a new file with no diff", gitlab.VersionDiff{OldPath: "a", NewPath: "a", AMode: "0", BMode: "100644", NewFile: true}, false},
		{"a deleted file with no diff", gitlab.VersionDiff{OldPath: "a", NewPath: "a", AMode: "100644", BMode: "0", DeletedFile: true}, false},
		{"a diff too large", gitlab.VersionDiff{OldPath: "a", NewPath: "a", TooLarge: true}, false},
		{"a diff too large, of a renamed file", gitlab.VersionDiff{OldPath: "a", NewPath: "b", RenamedFile: true, TooLarge: true}, false},
		{"a collapsed diff", gitlab.VersionDiff{OldPath: "a", NewPath: "a", Collapsed: true}, false},
		{"a collapsed diff, of a file whose mode changes", gitlab.VersionDiff{OldPath: "a", NewPath: "a", AMode: "100644", BMode: "100755", Collapsed: true}, false},

		{"a pure rename", gitlab.VersionDiff{OldPath: "a", NewPath: "b", AMode: "100644", BMode: "100644", RenamedFile: true}, true},
		{"a mode change alone", gitlab.VersionDiff{OldPath: "run", NewPath: "run", AMode: "100644", BMode: "100755"}, true},
		{"a text change", file("@@ -1 +1 @@\n-old\n+new\n"), true},
		{"a removed SQL comment", file("@@ -1 +0,0 @@\n-- a\n"), true},
	}
	for _, tt := range tests {
		if _, known := PatchID([]gitlab.VersionDiff{tt.d}); known != tt.known {
			t.Errorf("%s: known = %v, want %v", tt.name, known, tt.known)
		}
		// One file that cannot be seen makes the whole diff unknown.
		if _, known := PatchID([]gitlab.VersionDiff{file("@@ -1 +1 @@\n-old\n+new\n"), tt.d}); known != tt.known {
			t.Errorf("%s, after a text change: known = %v, want %v", tt.name, known, tt.known)
		}
	}
	if sum, known := PatchID(nil); !known || sum == "" {
		t.Errorf("no file at all: %q, %v; want a hash", sum, known)
	}
}

// What this hash calls a change and git patch-id a rebase: the target
// branch added lines above the hunk, in the same file. The safe side.
func TestPatchIDTakesShiftedLinesForAChange(t *testing.T) {
	a, _ := PatchID([]gitlab.VersionDiff{file("@@ -10,3 +10,3 @@ func f() {\n ctx\n-old\n+new\n ctx2\n")})
	b, _ := PatchID([]gitlab.VersionDiff{file("@@ -14,3 +14,3 @@ func f() {\n ctx\n-old\n+new\n ctx2\n")})
	if a == b {
		t.Error("the same hunk four lines lower hashes the same: a moved change would too")
	}
}

// compared is an answer of the compare API with these files.
func compared(t *testing.T, files ...map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"commits": []any{}, "diffs": files, "compare_timeout": false})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Through Differ.Events, on a GitLab whose patch IDs are not trusted:
// what the compare API answers decides, and anything that cannot be
// compared leaves rebase_only out, which ends a wait like a change.
func TestRebaseOnlyFromTheCompareAPI(t *testing.T) {
	text := map[string]any{"old_path": "a.go", "new_path": "a.go", "a_mode": "100644", "b_mode": "100644", "diff": sameDiff}
	with := func(k string, v any) map[string]any {
		out := map[string]any{}
		for key, val := range text {
			out[key] = val
		}
		out[k] = v
		return out
	}
	binary := map[string]any{"old_path": "logo.png", "new_path": "logo.png", "a_mode": "100644", "b_mode": "100644", "diff": "Binary files a/logo.png and b/logo.png differ\n"}
	tests := []struct {
		name       string
		old, newer string
		want       string
		trigger    string
	}{
		{name: "the same diff", old: compared(t, text), newer: compared(t, text), want: "true", trigger: "rebase"},
		{name: "the same diff, and the other fields of the real answer", old: compared(t, text),
			newer: compared(t, with("generated_file", false)), want: "true", trigger: "rebase"},
		{name: "chmod +x on a file that also changes", old: compared(t, text), newer: compared(t, with("b_mode", "100755")), want: "false", trigger: "push"},
		{name: "CRLF", old: compared(t, text), newer: compared(t, with("diff", "@@ -1,2 +1,2 @@\n ctx\n-old\n+new\r\n")), want: "false", trigger: "push"},
		{name: "no diffs key, twice", old: `{"commits": []}`, newer: `{"commits": []}`, want: "unknown", trigger: "push"},
		{name: "no diffs key in the old one", old: `{"commits": []}`, newer: compared(t, text), want: "unknown", trigger: "push"},
		{name: "no diffs key in the new one", old: compared(t, text), newer: `{}`, want: "unknown", trigger: "push"},
		{name: "null diffs, twice", old: `{"diffs": null}`, newer: `{"diffs": null}`, want: "unknown", trigger: "push"},
		{name: "compare_timeout, twice", old: `{"diffs": [], "compare_timeout": true}`, newer: `{"diffs": [], "compare_timeout": true}`, want: "unknown", trigger: "push"},
		{name: "a binary file, twice", old: compared(t, text, binary), newer: compared(t, text, binary), want: "unknown", trigger: "push"},
		{name: "a diff too large, twice", old: compared(t, with("too_large", true)), newer: compared(t, with("too_large", true)), want: "unknown", trigger: "push"},
		{name: "a collapsed diff, twice", old: compared(t, with("collapsed", true)), newer: compared(t, with("collapsed", true)), want: "unknown", trigger: "push"},
		{name: "the old commits are gone", newer: compared(t, text), want: "unknown", trigger: "push"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &differFake{release: "17.11.4", compare: map[string]string{"base2..head2": tt.newer}}
			if tt.old != "" {
				f.compare["base1..head1"] = tt.old
			}
			prev, s := pushed(patchA, patchA)
			e := onlyPush(t, &Differ{Client: f.client(t), People: People{Me: "me"}}, prev, s)
			if got := rebaseOnly(e); got != tt.want || e.Trigger() != tt.trigger {
				t.Errorf("rebase_only = %s, trigger %s; want %s, %s", got, e.Trigger(), tt.want, tt.trigger)
			}
			if ends := slices.Contains(DefaultTriggers, e.Trigger()); ends != (tt.want != "true") {
				t.Errorf("ends a wait by default = %v", ends)
			}
		})
	}
}
