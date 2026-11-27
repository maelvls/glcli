package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type installOutput struct {
	Skills []struct {
		Name    string `json:"name"`
		Status  string `json:"status"`
		Reason  string `json:"reason"`
		Differs bool   `json:"differs"`
	} `json:"skills"`
	Hints []string `json:"hints"`
}

func skillsInstall(t *testing.T, args ...string) installOutput {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := Run(append([]string{"skills", "install"}, args...), &stdout, &stderr); code != 0 {
		t.Fatalf("%v: exit %d (stderr: %s)", args, code, stderr.String())
	}
	var out installOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || len(out.Skills) == 0 {
		t.Fatalf("%v: stdout %q: %v", args, stdout.String(), err)
	}
	return out
}

type listOutput struct {
	Dir    string `json:"dir"`
	Skills []struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Files       []string `json:"files"`
		Installed   string   `json:"installed"`
		Symlink     string   `json:"symlink"`
		Error       string   `json:"error"`
	} `json:"skills"`
	Hints    []string `json:"hints"`
	Warnings []string `json:"warnings"`
}

func skillsList(t *testing.T, args ...string) listOutput {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := Run(append([]string{"skills", "list"}, args...), &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("%v: exit %d (stderr: %s)", args, code, stderr.String())
	}
	var out listOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || len(out.Skills) == 0 {
		t.Fatalf("%v: stdout %q: %v", args, stdout.String(), err)
	}
	// What the command printed before --dir existed is still there.
	if s := out.Skills[0]; s.Name != "glcli" || s.Description == "" || len(s.Files) == 0 {
		t.Fatalf("%v: got %+v", args, s)
	}
	return out
}

// `skills list` is how an agent checks that its skill is this glcli's: it
// must not install anything, which `skills install` does when no skill is
// there, nor create the directory it looks into.
func TestSkillsListComparesAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	out := skillsList(t, "--dir", dir)
	if s := out.Skills[0]; s.Installed != "absent" || out.Dir != dir || len(out.Hints)+len(out.Warnings) != 0 {
		t.Errorf("empty directory: %+v", out)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("empty directory: something was written: %v", entries)
	}

	missing := filepath.Join(dir, "no", "skills")
	if out = skillsList(t, "--dir", missing); out.Skills[0].Installed != "absent" || out.Dir != missing {
		t.Errorf("missing directory: %+v", out)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("missing directory: it was created: %v", entries)
	}

	// Without --dir: ~/.claude/skills, which is not created either.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if out = skillsList(t); out.Skills[0].Installed != "absent" || out.Dir != filepath.Join(home, ".claude", "skills") {
		t.Errorf("default directory: %+v", out)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("default directory: something was written in the home directory: %v", entries)
	}

	skillsInstall(t, "--dir", dir)
	if out = skillsList(t, "--dir", dir); out.Skills[0].Installed != "identical" || out.Skills[0].Symlink != "" {
		t.Errorf("identical copy: %+v", out)
	}
	linked := t.TempDir()
	skill := filepath.Join(dir, out.Skills[0].Name)
	if err := os.Symlink(skill, filepath.Join(linked, out.Skills[0].Name)); err != nil {
		t.Fatal(err)
	}
	if out = skillsList(t, "--dir", linked); out.Skills[0].Installed != "identical" || out.Skills[0].Symlink != skill {
		t.Errorf("symlink to an identical skill: %+v", out)
	}

	md := filepath.Join(skill, "SKILL.md")
	if err := os.WriteFile(md, []byte("an older skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out = skillsList(t, "--dir", dir); out.Skills[0].Installed != "differs" || out.Skills[0].Symlink != "" {
		t.Errorf("copy that differs: %+v", out)
	}
	if out = skillsList(t, "--dir", linked); out.Skills[0].Installed != "differs" || out.Skills[0].Symlink != skill {
		t.Errorf("symlink to a skill that differs: %+v", out)
	}
	if b, err := os.ReadFile(md); err != nil || string(b) != "an older skill" {
		t.Errorf("the installed skill was changed: %q, %v", b, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("something was written next to the skill: %v", entries)
	}
}

// The --overwrite hint is only for an installed copy that is not this
// glcli's: it would be noise for an identical one, and wrong for a
// symlink, which --overwrite leaves alone.
func TestSkillsInstallHintsOnlyWhenACopyDiffers(t *testing.T) {
	dir := t.TempDir()
	if out := skillsInstall(t, "--dir", dir); out.Skills[0].Status != "installed" || len(out.Hints) != 0 {
		t.Fatalf("first install: %+v", out)
	}
	out := skillsInstall(t, "--dir", dir)
	if s := out.Skills[0]; s.Status != "skipped" || s.Differs || s.Reason != "already installed, identical to this glcli's" || len(out.Hints) != 0 {
		t.Errorf("identical copy: %+v", out)
	}

	md := filepath.Join(dir, out.Skills[0].Name, "SKILL.md")
	if err := os.WriteFile(md, []byte("an older skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = skillsInstall(t, "--dir", dir)
	if s := out.Skills[0]; s.Status != "skipped" || !s.Differs || len(out.Hints) != 1 {
		t.Errorf("copy that differs: %+v", out)
	}
	if out = skillsInstall(t, "--dir", dir, "--overwrite"); out.Skills[0].Status != "replaced" || out.Skills[0].Differs || len(out.Hints) != 0 {
		t.Errorf("overwrite: %+v", out)
	}

	// The same skill that differs, behind a symlink.
	if err := os.WriteFile(md, []byte("an older skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	linked := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, out.Skills[0].Name), filepath.Join(linked, out.Skills[0].Name)); err != nil {
		t.Fatal(err)
	}
	out = skillsInstall(t, "--dir", linked)
	if s := out.Skills[0]; s.Status != "skipped" || !s.Differs || len(out.Hints) != 0 {
		t.Errorf("symlink to a skill that differs: %+v", out)
	}
}
