// Package skills embeds the agent skills shipped with glcli, so that the
// binary alone can install them ("glcli skills install").
package skills

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// FS holds one directory per skill, each with a SKILL.md. Add a new
// skill's directory to the pattern below.
//
//go:embed glcli
var FS embed.FS

// Skill is one embedded skill.
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Files       []string `json:"files"`
}

// List returns the embedded skills, sorted by name.
func List() ([]Skill, error) {
	return list(FS)
}

func list(fsys fs.FS) ([]Skill, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		md, err := fs.ReadFile(fsys, path.Join(e.Name(), "SKILL.md"))
		if err != nil {
			continue // not a skill
		}
		s := Skill{Name: e.Name(), Description: description(string(md))}
		err = fs.WalkDir(fsys, e.Name(), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				s.Files = append(s.Files, strings.TrimPrefix(p, e.Name()+"/"))
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// description returns the "description:" of a SKILL.md front matter.
func description(md string) string {
	rest, ok := strings.CutPrefix(md, "---\n")
	if !ok {
		return ""
	}
	front, _, _ := strings.Cut(rest, "\n---")
	for _, line := range strings.Split(front, "\n") {
		if v, ok := strings.CutPrefix(line, "description:"); ok {
			v = strings.TrimSpace(v)
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// Result says what Install did with one skill.
type Result struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Status is installed, replaced (--overwrite), or skipped.
	Status string `json:"status"`
	// Reason says why a skill was skipped.
	Reason string `json:"reason,omitempty"`
}

// Install writes the embedded skills into dir, one directory per skill.
// An existing skill is replaced only when overwrite is set; a skill that is
// a symlink (a checkout used for development) is always left alone. Each
// skill is written to a temporary directory first and then moved in, so a
// failure never leaves half a skill behind.
func Install(dir string, overwrite bool) ([]Result, error) {
	return install(FS, dir, overwrite)
}

func install(fsys fs.FS, dir string, overwrite bool) ([]Result, error) {
	all, err := list(fsys)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var results []Result
	for _, s := range all {
		dest := filepath.Join(dir, s.Name)
		r := Result{Name: s.Name, Path: dest, Status: "installed"}
		info, err := os.Lstat(dest)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return results, err
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(dest)
			r.Status, r.Reason = "skipped", "a symlink to "+target+" (left alone: remove it to install a copy)"
			results = append(results, r)
			continue
		case !overwrite:
			r.Status, r.Reason = "skipped", "already installed: pass --overwrite to replace it"
			results = append(results, r)
			continue
		default:
			r.Status = "replaced"
		}
		if err := writeSkill(fsys, s.Name, dir, dest); err != nil {
			return results, fmt.Errorf("installing %s: %w", s.Name, err)
		}
		results = append(results, r)
	}
	return results, nil
}

func writeSkill(fsys fs.FS, name, dir, dest string) error {
	tmp, err := os.MkdirTemp(dir, "."+name+".tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	err = fs.WalkDir(fsys, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(tmp, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(p, name), "/")))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
