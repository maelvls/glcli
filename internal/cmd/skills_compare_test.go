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
