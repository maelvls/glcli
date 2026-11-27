package skills

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

var fake = fstest.MapFS{
	"one/SKILL.md":           {Data: []byte("---\nname: one\ndescription: \"Does one thing.\"\n---\n\n# one\n")},
	"one/references/more.md": {Data: []byte("more")},
	"notaskill/README.md":    {Data: []byte("x")},
}

func TestEmbeddedSkills(t *testing.T) {
	all, err := List()
	if err != nil || len(all) == 0 || all[0].Name != "glcli" || all[0].Description == "" {
		t.Fatalf("embedded skills: %+v, %v", all, err)
	}
}

func TestList(t *testing.T) {
	all, err := list(fake)
	if err != nil || len(all) != 1 {
		t.Fatalf("got %+v, %v", all, err)
	}
	if s := all[0]; s.Name != "one" || s.Description != "Does one thing." || len(s.Files) != 2 {
		t.Fatalf("got %+v", s)
	}
}

func TestInstall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	res, err := install(fake, dir, false)
	if err != nil || len(res) != 1 || res[0].Status != "installed" {
		t.Fatalf("first install: %+v, %v", res, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "one", "references", "more.md")); err != nil || string(b) != "more" {
		t.Fatalf("nested file: %q, %v", b, err)
	}

	// A second install skips; --overwrite replaces and drops stale files.
	if err := os.WriteFile(filepath.Join(dir, "one", "stale.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ = install(fake, dir, false)
	if res[0].Status != "skipped" {
		t.Fatalf("second install: %+v", res)
	}
	res, err = install(fake, dir, true)
	if err != nil || res[0].Status != "replaced" {
		t.Fatalf("overwrite: %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "one", "stale.md")); !os.IsNotExist(err) {
		t.Fatal("--overwrite must remove files that are no longer shipped")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temporary directories left behind: %v", entries)
	}
}

func TestInstallLeavesSymlinkAlone(t *testing.T) {
	dir := t.TempDir()
	checkout := t.TempDir()
	if err := os.Symlink(checkout, filepath.Join(dir, "one")); err != nil {
		t.Fatal(err)
	}
	res, err := install(fake, dir, true)
	if err != nil || res[0].Status != "skipped" {
		t.Fatalf("got %+v, %v", res, err)
	}
	if entries, _ := os.ReadDir(checkout); len(entries) != 0 {
		t.Fatal("nothing may be written through the symlink")
	}
}
