// Package skills embeds the agent skills shipped with glcli, so that the
// binary alone can install them ("glcli skills install").
package skills

import (
	"bytes"
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
	// Reason says why a skill was skipped, and whether what is installed is
	// identical to the embedded skill.
	Reason string `json:"reason,omitempty"`
	// Differs is set on a skipped skill whose installed files are not the
	// embedded ones: it comes from another version of glcli, or was edited.
	Differs bool `json:"differs,omitempty"`
	// Symlink is set on a skill skipped because it is a symlink, which
	// overwrite does not replace.
	Symlink bool `json:"-"`
}

// Install writes the embedded skills into dir, one directory per skill.
// An existing skill is replaced only when overwrite is set; a skill that is
// a symlink (a checkout used for development) is always left alone. Each
// skill is written to a temporary directory first and then moved in, so a
// failure never leaves half a skill behind.
//
// A skill that is left alone is compared with the embedded one, so that
// the result tells whether the installed skill is this binary's without
// anything being written, neither a stamp nor a version file.
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
			r.Status, r.Symlink = "skipped", true
			compared := ""
			// A symlink that cannot be followed keeps the plain reason.
			if same, err := sameFiles(fsys, s.Name, dest); err == nil && same {
				compared = ", identical to this glcli's"
			} else if err == nil {
				compared, r.Differs = ", which differs from this glcli's", true
			}
			r.Reason = "a symlink to " + target + compared + " (left alone: remove it to install a copy)"
			results = append(results, r)
			continue
		case !overwrite:
			r.Status = "skipped"
			switch same, err := sameFiles(fsys, s.Name, dest); {
			case err != nil:
				r.Reason = "already installed, could not be compared with this glcli's (" + err.Error() + "): pass --overwrite to replace it"
			case same:
				r.Reason = "already installed, identical to this glcli's"
			default:
				r.Reason, r.Differs = "already installed, differs from this glcli's: pass --overwrite to replace it", true
			}
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

// sameFiles reports whether the directory dest holds the files of the
// embedded skill name and nothing else: same paths, same bytes. File modes
// and empty directories do not count.
func sameFiles(fsys fs.FS, name, dest string) (bool, error) {
	embedded, err := fs.Sub(fsys, name)
	if err != nil {
		return false, err
	}
	want, err := readFiles(embedded)
	if err != nil {
		return false, err
	}
	// os.DirFS follows dest when it is a symlink.
	got, err := readFiles(os.DirFS(dest))
	if err != nil {
		return false, err
	}
	if len(got) != len(want) {
		return false, nil
	}
	for p, data := range want {
		if installed, ok := got[p]; !ok || !bytes.Equal(installed, data) {
			return false, nil
		}
	}
	return true, nil
}

// readFiles returns the content of every file of fsys by path. A skill is
// a few small text files.
func readFiles(fsys fs.FS) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, p)
		files[p] = data
		return err
	})
	return files, err
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
