package config

import (
	"strings"
	"testing"
)

func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GLCLI_HOST", "")
	t.Setenv("GITLAB_HOST", "")
	t.Setenv("GLCLI_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "")
}

// glab can leave the !!null tag of a once empty value on the token it
// writes there: the token must be read all the same.
func TestParseTokenTaggedNull(t *testing.T) {
	clearEnv(t)
	c, err := Parse([]byte(`
host: !!null gitlab.example.com
hosts:
    gitlab.example.com:
        api_host: !!null api.example.com
        api_protocol: !!null http
        user: !!null me
        token: !!null glpat-SECRET
    gitlab.com:
        token: glpat-plain
`), "/x/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.Host("")
	if err != nil || h.Name != "gitlab.example.com" || h.Token != "glpat-SECRET" || h.User != "me" || h.BaseURL() != "http://api.example.com" {
		t.Fatalf("default host: %+v %v", h, err)
	}
	if h, err := c.Host("gitlab.com"); err != nil || h.Token != "glpat-plain" || h.BaseURL() != "https://gitlab.com" {
		t.Fatalf("gitlab.com: %+v %v", h, err)
	}
}

// A null token is no token, however it is written.
func TestParseNullTokenIsEmpty(t *testing.T) {
	clearEnv(t)
	for _, token := range []string{"", "!!null", "~", "null", "!!null ~", "!!null null", `""`, "!!str"} {
		c, err := Parse([]byte("hosts:\n    gitlab.com:\n        token: "+token+"\n        user: me\n"), "/x/config.yml")
		if err != nil {
			t.Fatalf("token: %s: %v", token, err)
		}
		if got := c.Hosts["gitlab.com"]; got.Token != "" || got.User != "me" || got.BaseURL() != "https://gitlab.com" {
			t.Fatalf("token: %s: %+v", token, got)
		}
		_, err = c.Host("gitlab.com")
		if err == nil || !strings.Contains(err.Error(), "no token for gitlab.com in /x/config.yml") {
			t.Fatalf("token: %s: expected the no token error, got %v", token, err)
		}
	}
}

// Whatever is wrong with the file, the error names the file and the line
// (the one yaml gives: line 1 for the bad indentation of line 4), and never
// repeats what the file holds.
func TestParseErrorDoesNotLeak(t *testing.T) {
	clearEnv(t)
	for name, tc := range map[string]struct{ data, want string }{
		"syntax":           {"hosts:\n    gitlab.com:\n        token: glpat-SECRET\n      user: me\n", "line 1"},
		"unclosed quote":   {"hosts:\n    gitlab.com:\n        token: \"glpat-SECRET\n", "line 3"},
		"token as hosts":   {"hosts: glpat-SECRET\n", "line 1"},
		"token as host":    {"hosts:\n    gitlab.com: glpat-SECRET\n", "line 2"},
		"token as list":    {"hosts:\n    gitlab.com:\n        token: [glpat-SECRET]\n", "line 3: token is not a string"},
		"token as mapping": {"hosts:\n    gitlab.com:\n        token:\n            glpat-SECRET: glpat-SECRET\n", "line 4: token is not a string"},
		"duplicate token":  {"hosts:\n    gitlab.com:\n        token: !!null glpat-SECRET\n        token: glpat-SECRET2\n", "line 4"},
		"two bad fields":   {"hosts:\n    b.example.com:\n        token: [glpat-SECRET]\n    a.example.com:\n        user: [glpat-SECRET]\n", "line 3: token is not a string"},
		"duplicate host":   {"hosts:\n    glpat-SECRET:\n        user: a\n    glpat-SECRET:\n        user: b\n", "line 4"},
		"unknown anchor":   {"hosts:\n    gitlab.com:\n        token: *glpat-SECRET\n", ""},
		"top-level scalar": {"glpat-SECRET\n", "line 1"},
	} {
		c, err := Parse([]byte(tc.data), "/x/config.yml")
		if err == nil {
			t.Fatalf("%s: expected an error, got %+v", name, c)
		}
		msg := err.Error()
		if strings.Contains(msg, "SECRET") {
			t.Errorf("%s: the error leaks the content of the file: %s", name, msg)
		}
		if !strings.HasPrefix(msg, "reading /x/config.yml: ") || !strings.Contains(msg, tc.want) {
			t.Errorf("%s: expected the path and %q, got: %s", name, tc.want, msg)
		}
	}
}

// An anchor on a token is still followed, as it was when the fields were
// strings.
func TestParseTokenAlias(t *testing.T) {
	clearEnv(t)
	c, err := Parse([]byte("hosts:\n    a.example.com:\n        token: &t glpat-shared\n    b.example.com:\n        token: *t\n"), "/x/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	if h, err := c.Host("b.example.com"); err != nil || h.Token != "glpat-shared" {
		t.Fatalf("b.example.com: %+v %v", h, err)
	}
}

func TestParseSampleStillParses(t *testing.T) {
	clearEnv(t)
	c, err := Parse([]byte(sample), "/x/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.Names(), " "); got != "code.pan.run gitlab.com" || c.DefaultHost != "gitlab.com" {
		t.Fatalf("hosts %q, default %q", got, c.DefaultHost)
	}
	if h := c.Hosts["gitlab.com"]; h.Token != "glpat-aaa" || h.User != "me" || h.TokenSource != "/x/config.yml" {
		t.Fatalf("gitlab.com: %+v", h)
	}
}
