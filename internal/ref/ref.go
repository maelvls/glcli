// Package ref parses the ways an agent names a merge request or a
// pipeline: a web URL, "group/project!123", or "!123" in a git checkout.
package ref

import (
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
)

// MR names a merge request.
type MR struct {
	Host    string `json:"host"`
	Project string `json:"project"`
	IID     int    `json:"iid"`
}

func (m MR) String() string { return m.Project + "!" + strconv.Itoa(m.IID) }

// Pipeline names a pipeline.
type Pipeline struct {
	Host    string
	Project string
	ID      int
}

// Defaults fill in what a short reference leaves out.
type Defaults struct {
	// Host is the host to use when nothing else names one (--host or the
	// default host of glab's config).
	Host string
	// HostFlag is --host: it wins over the git remote's host.
	HostFlag string
	// Repo is --repo: a project path or URL.
	Repo string
	// Remote returns the host and project of the git checkout in the
	// current directory.
	Remote func() (host, project string, err error)
}

// ParseMR parses a merge request reference:
//
//	https://gitlab.com/group/project/-/merge_requests/123[/diffs][#note_1]
//	group/project!123
//	!123 or 123 (project from --repo or the git remote of the current directory)
func ParseMR(s string, d Defaults) (MR, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") {
		host, project, kind, id, err := parseWebURL(s)
		if err != nil {
			return MR{}, err
		}
		if kind != "merge_requests" {
			return MR{}, fmt.Errorf("%q is not a merge request URL", s)
		}
		return MR{Host: host, Project: project, IID: id}, nil
	}
	project, num, hasBang := strings.Cut(s, "!")
	if !hasBang {
		num, project = s, ""
	}
	iid, err := strconv.Atoi(num)
	if err != nil || iid <= 0 {
		return MR{}, fmt.Errorf("cannot read %q as a merge request: use its URL, group/project!123 or !123", s)
	}
	host, project, err := resolveProject(project, d)
	if err != nil {
		return MR{}, err
	}
	return MR{Host: host, Project: project, IID: iid}, nil
}

// ParsePipeline parses a pipeline URL (https://host/group/project/-/pipelines/123).
// ok is false when s is not one, so that the caller can try ParseMR.
func ParsePipeline(s string) (p Pipeline, ok bool, err error) {
	if !strings.Contains(s, "://") || !strings.Contains(s, "/-/pipelines/") {
		return Pipeline{}, false, nil
	}
	host, project, kind, id, err := parseWebURL(s)
	if err != nil {
		return Pipeline{}, true, err
	}
	if kind != "pipelines" {
		return Pipeline{}, false, nil
	}
	return Pipeline{Host: host, Project: project, ID: id}, true, nil
}

// ParseProject turns a project path or URL into a host (possibly empty)
// and a project path.
func ParseProject(s string) (host, project string) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			p := strings.Trim(u.Path, "/")
			p, _, _ = strings.Cut(p, "/-/")
			return u.Host, strings.TrimSuffix(p, ".git")
		}
	}
	return "", strings.Trim(s, "/")
}

func parseWebURL(s string) (host, project, kind string, id int, err error) {
	u, err := url.Parse(s)
	if err != nil {
		return "", "", "", 0, fmt.Errorf("cannot parse URL %q: %w", s, err)
	}
	path := strings.Trim(u.Path, "/")
	project, rest, ok := strings.Cut(path, "/-/")
	if !ok || project == "" {
		return "", "", "", 0, fmt.Errorf("%q is not a GitLab project URL (no /-/ in it)", s)
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return "", "", "", 0, fmt.Errorf("%q has no ID after /-/%s", s, rest)
	}
	id, err = strconv.Atoi(parts[1])
	if err != nil {
		return "", "", "", 0, fmt.Errorf("%q: %q is not a number", s, parts[1])
	}
	return u.Host, project, parts[0], id, nil
}

func resolveProject(project string, d Defaults) (host, path string, err error) {
	host = d.HostFlag
	if project == "" && d.Repo != "" {
		h, p := ParseProject(d.Repo)
		if host == "" {
			host = h
		}
		project = p
	}
	if project == "" {
		if d.Remote == nil {
			return "", "", fmt.Errorf("no project: pass group/project!N, --repo, or run inside a git checkout")
		}
		h, p, err := d.Remote()
		if err != nil {
			return "", "", fmt.Errorf("no project given and none found in the git remote of the current directory (%v): pass group/project!N or --repo", err)
		}
		if host == "" {
			host = h
		}
		project = p
	}
	if host == "" {
		host = d.Host
	}
	return host, project, nil
}

// GitRemote returns the host and project of the "origin" remote of the
// git checkout in the current directory (or of its only remote).
func GitRemote() (host, project string, err error) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		names, err2 := exec.Command("git", "remote").Output()
		if err2 != nil {
			return "", "", fmt.Errorf("not a git checkout")
		}
		first, _, _ := strings.Cut(strings.TrimSpace(string(names)), "\n")
		if first == "" {
			return "", "", fmt.Errorf("the git checkout has no remote")
		}
		if out, err = exec.Command("git", "remote", "get-url", first).Output(); err != nil {
			return "", "", fmt.Errorf("git remote get-url %s: %w", first, err)
		}
	}
	raw := strings.TrimSpace(string(out))
	h, p, ok := ParseRemote(raw)
	if !ok {
		return "", "", remoteError(raw)
	}
	return h, p, nil
}

// remoteError is the error for a remote URL that ParseRemote cannot read.
// A remote can hold a token (https://oauth2:glpat-…@host/…), so what
// precedes its last "@" is never shown. It is cut by hand: url.Parse fails
// on these URLs.
func remoteError(raw string) error {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return fmt.Errorf("cannot read the remote URL %q", raw)
	}
	scheme := ""
	for _, s := range []string{"https://", "http://", "ssh://", "git://"} {
		if strings.HasPrefix(raw, s) {
			scheme = s
		}
	}
	return fmt.Errorf("cannot read the remote URL %q (what precedes the @ is left out)", scheme+raw[at+1:])
}

// ParseRemote reads a git remote URL: git@host:group/project.git,
// ssh://git@host[:port]/group/project.git or https://host/group/project.git.
func ParseRemote(s string) (host, project string, ok bool) {
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", "", false
		}
		host = u.Hostname()
		if u.Scheme == "http" || u.Scheme == "https" {
			host = u.Host
		}
		project = strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		return host, project, project != ""
	}
	at := strings.Index(s, "@")
	colon := strings.Index(s, ":")
	if colon < 0 || colon < at {
		return "", "", false
	}
	host = s[at+1 : colon]
	// "a@glpat-…@host:group/project" would make the token part of the host.
	if strings.Contains(host, "@") {
		return "", "", false
	}
	project = strings.TrimSuffix(strings.Trim(s[colon+1:], "/"), ".git")
	return host, project, host != "" && project != ""
}
