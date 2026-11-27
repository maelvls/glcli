package skills

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A second install without --overwrite writes nothing, and says whether
// the installed skill is the embedded one.
func TestInstallComparesASkippedSkill(t *testing.T) {
	for _, tc := range []struct {
		name string
		// change edits the installed skill, given its directory.
		change  func(t *testing.T, skill string)
		differs bool
		reason  string
	}{
		{
			name:   "untouched",
			change: func(*testing.T, string) {},
			reason: "already installed, identical to this glcli's",
		},
		{
			name: "an empty directory more",
			change: func(t *testing.T, skill string) {
				mkdir(t, filepath.Join(skill, "empty"))
			},
			reason: "already installed, identical to this glcli's",
		},
		{
			// Same length: only the bytes tell.
			name: "a changed byte",
			change: func(t *testing.T, skill string) {
				write(t, filepath.Join(skill, "references", "more.md"), "mare")
			},
			differs: true,
			reason:  "already installed, differs from this glcli's: pass --overwrite to replace it",
		},
		{
			name: "a missing file",
			change: func(t *testing.T, skill string) {
				if err := os.Remove(filepath.Join(skill, "references", "more.md")); err != nil {
					t.Fatal(err)
				}
			},
			differs: true,
			reason:  "already installed, differs from this glcli's: pass --overwrite to replace it",
		},
		{
			name: "an extra file",
			change: func(t *testing.T, skill string) {
				write(t, filepath.Join(skill, "notes.md"), "mine")
			},
			differs: true,
			reason:  "already installed, differs from this glcli's: pass --overwrite to replace it",
		},
		{
			// As many files as embedded, under another name.
			name: "a renamed file",
			change: func(t *testing.T, skill string) {
				if err := os.Rename(filepath.Join(skill, "references", "more.md"), filepath.Join(skill, "more.md")); err != nil {
					t.Fatal(err)
				}
			},
			differs: true,
			reason:  "already installed, differs from this glcli's: pass --overwrite to replace it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := install(fake, dir, false); err != nil {
				t.Fatal(err)
			}
			skill := filepath.Join(dir, "one")
			tc.change(t, skill)
			before := snapshot(t, skill)

			res, err := install(fake, dir, false)
			if err != nil || len(res) != 1 {
				t.Fatalf("got %+v, %v", res, err)
			}
			if r := res[0]; r.Status != "skipped" || r.Differs != tc.differs || r.Reason != tc.reason || r.Symlink {
				t.Errorf("got %+v, want skipped, differs %v, reason %q", r, tc.differs, tc.reason)
			}
			if after := snapshot(t, skill); after != before {
				t.Errorf("the comparison changed the installed skill:\nbefore %s\nafter  %s", before, after)
			}

			// --overwrite still replaces, and what it wrote is identical.
			res, err = install(fake, dir, true)
			if err != nil || res[0].Status != "replaced" || res[0].Differs || res[0].Reason != "" {
				t.Fatalf("overwrite: %+v, %v", res, err)
			}
			res, err = install(fake, dir, false)
			if err != nil || res[0].Status != "skipped" || res[0].Differs {
				t.Fatalf("after overwrite: %+v, %v", res, err)
			}
		})
	}
}

// A symlinked skill is never replaced, with or without --overwrite, but
// the result says whether the checkout it points to is the embedded skill.
func TestInstallComparesASymlinkedSkill(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		dir := t.TempDir()
		checkout := filepath.Join(t.TempDir(), "checkout")
		if _, err := install(fake, filepath.Dir(checkout), false); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(filepath.Dir(checkout), "one"), checkout); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(checkout, filepath.Join(dir, "one")); err != nil {
			t.Fatal(err)
		}

		res, err := install(fake, dir, overwrite)
		if err != nil || len(res) != 1 {
			t.Fatalf("got %+v, %v", res, err)
		}
		if r := res[0]; r.Status != "skipped" || r.Differs || !r.Symlink || !strings.Contains(r.Reason, ", identical to this glcli's (left alone") {
			t.Errorf("overwrite %v, identical checkout: %+v", overwrite, r)
		}

		write(t, filepath.Join(checkout, "SKILL.md"), "edited in the checkout")
		before := snapshot(t, checkout)
		res, err = install(fake, dir, overwrite)
		if err != nil || len(res) != 1 {
			t.Fatalf("got %+v, %v", res, err)
		}
		if r := res[0]; r.Status != "skipped" || !r.Differs || !r.Symlink || !strings.Contains(r.Reason, ", which differs from this glcli's (left alone") {
			t.Errorf("overwrite %v, edited checkout: %+v", overwrite, r)
		}
		if after := snapshot(t, checkout); after != before {
			t.Errorf("overwrite %v: the checkout was written through the symlink", overwrite)
		}
	}
}

// What cannot be read is not said to be identical, nor to differ.
func TestInstallCannotCompare(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "one")); err != nil {
		t.Fatal(err)
	}
	res, err := install(fake, dir, false)
	if err != nil || len(res) != 1 {
		t.Fatalf("got %+v, %v", res, err)
	}
	want := "a symlink to " + filepath.Join(dir, "gone") + " (left alone: remove it to install a copy)"
	if r := res[0]; r.Status != "skipped" || r.Differs || r.Reason != want {
		t.Errorf("dangling symlink: %+v, want reason %q", r, want)
	}

	// A file where the skill's directory should be.
	dir = t.TempDir()
	write(t, filepath.Join(dir, "one"), "not a directory")
	res, err = install(fake, dir, false)
	if err != nil || len(res) != 1 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if r := res[0]; r.Status != "skipped" || r.Differs || !strings.HasPrefix(r.Reason, "already installed, could not be compared with this glcli's (") {
		t.Errorf("a file instead of a directory: %+v", r)
	}
}

// compare is the check an agent runs to know whether its skill is this
// binary's: it must never write, even where nothing is installed.
func TestCompareOnlyReads(t *testing.T) {
	embedded, err := list(fake)
	if err != nil {
		t.Fatal(err)
	}
	one := func(t *testing.T, dir string) Skill {
		t.Helper()
		all, err := compare(fake, dir)
		if err != nil || len(all) != 1 {
			t.Fatalf("got %+v, %v", all, err)
		}
		// What List gives is still there, unchanged.
		if s := all[0]; s.Name != embedded[0].Name || s.Description != embedded[0].Description || !slices.Equal(s.Files, embedded[0].Files) {
			t.Fatalf("got %+v, want the fields of %+v", s, embedded[0])
		}
		return all[0]
	}

	t.Run("a directory that does not exist", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "no", "skills")
		if s := one(t, dir); s.Installed != "absent" || s.Symlink != "" || s.Error != "" {
			t.Errorf("got %+v", s)
		}
		if _, err := os.Lstat(filepath.Dir(dir)); !os.IsNotExist(err) {
			t.Errorf("the directory was created (%v)", err)
		}
	})

	t.Run("an empty directory", func(t *testing.T) {
		dir := t.TempDir()
		if s := one(t, dir); s.Installed != "absent" || s.Symlink != "" || s.Error != "" {
			t.Errorf("got %+v", s)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("something was written: %v", entries)
		}
	})

	t.Run("a copy", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := install(fake, dir, false); err != nil {
			t.Fatal(err)
		}
		skill := filepath.Join(dir, "one")
		if s := one(t, dir); s.Installed != "identical" || s.Symlink != "" || s.Error != "" {
			t.Errorf("identical: got %+v", s)
		}
		for name, change := range map[string]func(){
			"a changed byte": func() { write(t, filepath.Join(skill, "references", "more.md"), "mare") },
			"an extra file":  func() { write(t, filepath.Join(skill, "notes.md"), "mine") },
			"a missing file": func() {
				if err := os.Remove(filepath.Join(skill, "SKILL.md")); err != nil {
					t.Fatal(err)
				}
			},
		} {
			if _, err := install(fake, dir, true); err != nil {
				t.Fatal(err)
			}
			change()
			before := snapshot(t, skill)
			if s := one(t, dir); s.Installed != "differs" || s.Symlink != "" || s.Error != "" {
				t.Errorf("%s: got %+v", name, s)
			}
			if after := snapshot(t, skill); after != before {
				t.Errorf("%s: the installed skill changed:\nbefore %s\nafter  %s", name, before, after)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Errorf("%s: something was written next to the skill: %v", name, entries)
			}
		}
	})

	t.Run("a symlink", func(t *testing.T) {
		dir := t.TempDir()
		elsewhere := t.TempDir()
		if _, err := install(fake, elsewhere, false); err != nil {
			t.Fatal(err)
		}
		checkout := filepath.Join(elsewhere, "one")
		if err := os.Symlink(checkout, filepath.Join(dir, "one")); err != nil {
			t.Fatal(err)
		}
		if s := one(t, dir); s.Installed != "identical" || s.Symlink != checkout || s.Error != "" {
			t.Errorf("identical checkout: got %+v", s)
		}
		write(t, filepath.Join(checkout, "SKILL.md"), "edited in the checkout")
		before := snapshot(t, checkout)
		if s := one(t, dir); s.Installed != "differs" || s.Symlink != checkout || s.Error != "" {
			t.Errorf("edited checkout: got %+v", s)
		}
		if after := snapshot(t, checkout); after != before {
			t.Error("the checkout was written through the symlink")
		}
		// Gone: neither identical nor different.
		if err := os.RemoveAll(checkout); err != nil {
			t.Fatal(err)
		}
		if s := one(t, dir); s.Installed != "unreadable" || s.Symlink != checkout || s.Error == "" {
			t.Errorf("dangling symlink: got %+v", s)
		}
	})

	t.Run("a file instead of a directory", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "one"), "not a directory")
		if s := one(t, dir); s.Installed != "unreadable" || s.Error == "" {
			t.Errorf("the skill is a file: got %+v", s)
		}
		// The skills directory itself is a file.
		if s := one(t, filepath.Join(dir, "one")); s.Installed != "unreadable" || s.Error == "" {
			t.Errorf("the skills directory is a file: got %+v", s)
		}
	})
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// snapshot lists every file under dir with its content, to check that
// nothing was written.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	files, err := readFiles(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, p := range slices.Sorted(maps.Keys(files)) {
		b.WriteString(p + "=" + string(files[p]) + ";")
	}
	return b.String()
}
