// Package config reads the hosts and tokens that glab already stores, so
// that glcli needs no login of its own.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

type rawConfig struct {
	Host  string `yaml:"host"`
	Hosts map[string]struct {
		APIHost     string `yaml:"api_host"`
		APIProtocol string `yaml:"api_protocol"`
		Token       string `yaml:"token"`
		User        string `yaml:"user"`
	} `yaml:"hosts"`
}

// Parse parses the content of glab's config.yml.
func Parse(data []byte, path string) (*Config, error) {
	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	c := &Config{Path: path, DefaultHost: raw.Host, Hosts: map[string]Host{}}
	if c.DefaultHost == "" {
		c.DefaultHost = "gitlab.com"
	}
	for name, h := range raw.Hosts {
		host := Host{
			Name:        name,
			APIHost:     h.APIHost,
			Protocol:    h.APIProtocol,
			Token:       h.Token,
			User:        h.User,
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
	return c, nil
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
