package config

import "testing"

const sample = `
host: gitlab.com
hosts:
    gitlab.com:
        api_host: gitlab.com
        api_protocol: https
        user: me
        token: glpat-aaa
    code.pan.run:
        token: glpat-bbb
        user: other
`

func TestParseAndHost(t *testing.T) {
	t.Setenv("GLCLI_HOST", "")
	t.Setenv("GITLAB_HOST", "")
	t.Setenv("GLCLI_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "")
	c, err := Parse([]byte(sample), "/x/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.Host("")
	if err != nil || h.Name != "gitlab.com" || h.Token != "glpat-aaa" || h.User != "me" || h.BaseURL() != "https://gitlab.com" {
		t.Fatalf("default host: %+v %v", h, err)
	}
	h, err = c.Host("https://code.pan.run/")
	if err != nil || h.APIHost != "code.pan.run" || h.Protocol != "https" || h.Token != "glpat-bbb" {
		t.Fatalf("code.pan.run: %+v %v", h, err)
	}
	if _, err := c.Host("example.com"); err == nil {
		t.Fatal("expected an error for a host without a token")
	}
}

// The environment token must only go to the default host.
func TestEnvTokenOnlyForDefaultHost(t *testing.T) {
	t.Setenv("GLCLI_HOST", "")
	t.Setenv("GITLAB_HOST", "")
	t.Setenv("GLCLI_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "glpat-env")
	c, _ := Parse([]byte(sample), "/x/config.yml")
	if h, _ := c.Host("gitlab.com"); h.Token != "glpat-env" || h.TokenSource != "$GITLAB_TOKEN" {
		t.Fatalf("default host should use the env token: %+v", h)
	}
	if h, _ := c.Host("code.pan.run"); h.Token != "glpat-bbb" {
		t.Fatalf("other host must keep its own token, got %q", h.Token)
	}
	t.Setenv("GLCLI_HOST", "code.pan.run")
	if h, _ := c.Host("gitlab.com"); h.Token != "glpat-aaa" {
		t.Fatalf("gitlab.com is no longer the default: must keep its token, got %q", h.Token)
	}
}
