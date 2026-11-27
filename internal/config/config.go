// Package config reads the hosts and tokens that glab already stores, so
// that glcli needs no login of its own.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Host is one GitLab instance glab knows about.
type Host struct {
	// Name is the key under "hosts:" in glab's config, such as gitlab.com.
	Name     string
	APIHost  string
	Protocol string
	Token    string
	// User is the username glab recorded at login. It can be empty.
	User string
	// TokenSource says where the token came from, for error messages.
	TokenSource string
}

// BaseURL is the scheme and host of the instance, without a trailing slash.
func (h Host) BaseURL() string {
	return h.Protocol + "://" + h.APIHost
}

// Config is glab's config.yml, reduced to what glcli uses.
type Config struct {
	Path        string
	DefaultHost string
	Hosts       map[string]Host
}

// Path returns where glab keeps its config: $GLAB_CONFIG_DIR/config.yml,
// $XDG_CONFIG_HOME/glab-cli/config.yml, or ~/.config/glab-cli/config.yml.
func Path() string {
	if d := os.Getenv("GLAB_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "config.yml")
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "glab-cli", "config.yml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "glab-cli", "config.yml")
}

// Load reads glab's config. A missing file is not an error: the config is
// then empty, and only the environment can supply a host and a token.
func Load() (*Config, error) {
	p := Path()
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{Path: p, DefaultHost: "gitlab.com", Hosts: map[string]Host{}}, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(data, p)
}

// The scalars are kept as nodes, which are not checked against their tag:
// glab can write "token: !!null glpat-…", and decoding that into a string
// fails with an error that quotes the token.
type rawConfig struct {
	Host  yaml.Node `yaml:"host"`
	Hosts map[string]struct {
		APIHost     yaml.Node `yaml:"api_host"`
		APIProtocol yaml.Node `yaml:"api_protocol"`
		Token       yaml.Node `yaml:"token"`
		User        yaml.Node `yaml:"user"`
	} `yaml:"hosts"`
}

// Parse parses the content of glab's config.yml.
func Parse(data []byte, path string) (*Config, error) {
	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("reading %s: %s", path, redact(err))
	}
	// The field reported is the first bad one of the file: the hosts are
	// visited in no fixed order.
	var bad *yaml.Node
	var badField string
	str := func(field string, n yaml.Node) string {
		s, ok := text(&n)
		if !ok && (bad == nil || n.Line < bad.Line) {
			bad, badField = &n, field
		}
		return s
	}
	c := &Config{Path: path, DefaultHost: str("host", raw.Host), Hosts: map[string]Host{}}
	if c.DefaultHost == "" {
		c.DefaultHost = "gitlab.com"
	}
	for name, h := range raw.Hosts {
		host := Host{
			Name:        name,
			APIHost:     str("api_host", h.APIHost),
			Protocol:    str("api_protocol", h.APIProtocol),
			Token:       str("token", h.Token),
			User:        str("user", h.User),
			TokenSource: path,
		}
		if host.APIHost == "" {
			host.APIHost = name
		}
		if host.Protocol == "" {
			host.Protocol = "https"
		}
		c.Hosts[name] = host
	}
	if bad != nil {
		return nil, fmt.Errorf("reading %s: line %d: %s is not a string", path, bad.Line, badField)
	}
	return c, nil
}

// text returns the text of a scalar whatever its tag says, so that
// "!!null glpat-…" reads like "glpat-…". A missing key and a real null
// ("token:", "token: ~", "token: !!null") are the empty string. ok is false
// for a list or a mapping.
func text(n *yaml.Node) (s string, ok bool) {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	if n.IsZero() {
		return "", true
	}
	if n.Kind != yaml.ScalarNode {
		return "", false
	}
	if n.Tag == "!!null" {
		switch n.Value {
		case "~", "null", "Null", "NULL":
			return "", true
		}
	}
	return n.Value, true
}

var yamlLine = regexp.MustCompile(`^(?:yaml: )?line (\d+):`)

// redact reduces an error of the yaml package to the lines it points at:
// its messages quote values of the file ("cannot unmarshal !!str `glpat-…`
// into …"), and the file holds tokens. "near", because for a syntax error
// the line can be the one where the enclosing block starts.
func redact(err error) string {
	msgs := []string{err.Error()}
	var te *yaml.TypeError
	if errors.As(err, &te) {
		msgs = te.Errors
	}
	var lines []string
	for _, msg := range msgs {
		if m := yamlLine.FindStringSubmatch(msg); m != nil && !slices.Contains(lines, m[1]) {
			lines = append(lines, m[1])
		}
	}
	switch len(lines) {
	case 0:
		return "invalid YAML"
	case 1:
		return "invalid YAML near line " + lines[0]
	}
	return "invalid YAML near lines " + strings.Join(lines, ", ")
}

// Default returns the name of the host to use when nothing else says:
// $GLCLI_HOST, $GITLAB_HOST, or glab's "host:".
func (c *Config) Default() string {
	for _, env := range []string{"GLCLI_HOST", "GITLAB_HOST"} {
		if v := os.Getenv(env); v != "" {
			return normalizeHost(v)
		}
	}
	return c.DefaultHost
}

// Host returns the host called name ("" for the default one), with its
// token. $GLCLI_TOKEN, then $GITLAB_TOKEN, replace the token of the default
// host only, so that a token is never sent to an instance it was not meant
// for.
func (c *Config) Host(name string) (Host, error) {
	if name == "" {
		name = c.Default()
	}
	name = normalizeHost(name)
	h, ok := c.Hosts[name]
	if !ok {
		h = Host{Name: name, APIHost: name, Protocol: "https"}
	}
	if name == c.Default() {
		for _, env := range []string{"GLCLI_TOKEN", "GITLAB_TOKEN"} {
			if v := os.Getenv(env); v != "" {
				h.Token, h.TokenSource = v, "$"+env
				break
			}
		}
	}
	if h.Token == "" {
		known := c.Names()
		return h, fmt.Errorf("no token for %s in %s (hosts there: %s): run `glab auth login --hostname %s`", name, c.Path, strings.Join(known, ", "), name)
	}
	return h, nil
}

// Names lists the configured hosts, sorted.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Hosts))
	for n := range c.Hosts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func normalizeHost(s string) string {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	return strings.TrimSuffix(s, "/")
}
