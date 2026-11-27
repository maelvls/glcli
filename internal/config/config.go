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
	"time"

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
	// OAuth says that glab logged in to this host with OAuth ("is_oauth2"
	// is "true" in its config): Token is then an OAuth 2.0 access token,
	// which GitLab takes in "Authorization: Bearer" and not in
	// "PRIVATE-TOKEN", which expires, and which glab renews (glcli never
	// does). It is false for a token that came from the environment.
	OAuth bool
	// Expiry is when glab's config says the OAuth access token expires
	// ("oauth2_expiry_date"). It is zero when the config does not say, or
	// says it in a way glcli cannot read.
	Expiry time.Time
	// Keyring says that glab keeps the token of this host in the keyring
	// of the operating system ("use_keyring" is "true"), where glcli does
	// not look, rather than in its config.
	Keyring bool
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
//
// "oauth2_refresh_token", which glab writes next to the token of an OAuth
// host, has no field here on purpose: glcli has no use for it.
type rawConfig struct {
	Host  yaml.Node `yaml:"host"`
	Hosts map[string]struct {
		APIHost     yaml.Node `yaml:"api_host"`
		APIProtocol yaml.Node `yaml:"api_protocol"`
		Token       yaml.Node `yaml:"token"`
		User        yaml.Node `yaml:"user"`
		IsOAuth2    yaml.Node `yaml:"is_oauth2"`
		Expiry      yaml.Node `yaml:"oauth2_expiry_date"`
		UseKeyring  yaml.Node `yaml:"use_keyring"`
	} `yaml:"hosts"`
}

// Parse parses the content of glab's config.yml. Its errors name the file,
// a line and what is wrong there, and never repeat a value or a key of the
// file, which holds tokens. (The names of the hosts and of their users are
// not secrets: Config.Host and `glcli auth status` do show them.)
func Parse(data []byte, path string) (*Config, error) {
	fail := func(msg string) (*Config, error) {
		return nil, fmt.Errorf("reading %s: %s", path, msg)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fail(redact(err))
	}
	var raw rawConfig
	if err := doc.Decode(&raw); err != nil {
		if msg := diagnose(&doc); msg != "" {
			return fail(msg)
		}
		return fail(redact(err))
	}
	// The field reported is the first bad one of the file: the hosts are
	// visited in no fixed order.
	var bad *yaml.Node
	var badField string
	str := func(field string, n yaml.Node) string {
		s, ok := text(&n)
		if !ok && (bad == nil || before(&n, bad)) {
			bad, badField = &n, field
		}
		return s
	}
	// What glab writes about its OAuth logins and its keyring is read the
	// way glab reads it: the text of the scalar, compared with "true", so
	// that "true" in quotes (what glab writes) and the boolean true are the
	// same. Anything else there is "no", and never an error: a file that
	// was read before glcli looked at these keys is still read.
	flag := func(n yaml.Node) bool {
		s, _ := text(&n)
		return s == "true"
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
			OAuth:       flag(h.IsOAuth2),
			Keyring:     flag(h.UseKeyring),
		}
		if host.OAuth {
			s, _ := text(&h.Expiry)
			host.Expiry = parseExpiry(s)
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
		return fail(fmt.Sprintf("line %d: %s is not a string", bad.Line, badField))
	}
	return c, nil
}

// parseExpiry reads "oauth2_expiry_date" in the three layouts glab itself
// accepts: RFC 3339, which it writes, and the two RFC 822 ones that its
// older versions wrote. A date that cannot be read is the zero time, as
// when there is none. An RFC 822 date with a zone name ("CET") is only
// kept when Go knows the offset of that name on this machine: otherwise
// Go reads it as UTC, hours away from what was meant.
func parseExpiry(s string) time.Time {
	for _, layout := range []string{time.RFC3339, time.RFC822, time.RFC822Z} {
		t, err := time.Parse(layout, s)
		if err != nil {
			continue
		}
		if layout == time.RFC822 && t.Location() != time.UTC && t.Location() != time.Local {
			return time.Time{}
		}
		return t
	}
	return time.Time{}
}

// text returns the text of a scalar whatever its tag says, so that
// "!!null glpat-…" reads like "glpat-…". A missing key and a real null
// ("token:", "token: ~", "token: !!null") are the empty string. ok is false
// for a list or a mapping.
func text(n *yaml.Node) (s string, ok bool) {
	n = target(n)
	if n.IsZero() || isNull(n) {
		return "", true
	}
	if n.Kind != yaml.ScalarNode {
		return "", false
	}
	return n.Value, true
}

// target follows an alias ("token: *t") to the node it stands for.
func target(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		return n.Alias
	}
	return n
}

// isNull is true for a real null, and false for a text that only carries
// the !!null tag.
func isNull(n *yaml.Node) bool {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!null" {
		return false
	}
	switch n.Value {
	case "", "~", "null", "Null", "NULL":
		return true
	}
	return false
}

// before says whether a comes first in the file.
func before(a, b *yaml.Node) bool {
	return a.Line < b.Line || a.Line == b.Line && a.Column < b.Column
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
// for. A token from the environment is not an OAuth one, whatever glab's
// config says of the host (glab does the same with $GITLAB_TOKEN).
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
				h.OAuth, h.Expiry = false, time.Time{}
				break
			}
		}
	}
	if h.Token == "" && ok {
		return h, c.noTokenInFile(h)
	}
	if h.Token == "" {
		known := c.Names()
		return h, fmt.Errorf("no token for %s in %s (hosts there: %s): run %s", name, c.Path, strings.Join(known, ", "), LoginAdvice(name))
	}
	return h, nil
}

// LoginAdvice is the glab command that gives glcli a token for host, for
// the messages that tell to log in. A plain `glab auth login` is not
// enough with a current glab: since 1.111.0 it keeps the token in the
// keyring of the operating system when there is one, where glcli does not
// read it, so that the user would log in and find glcli failing again.
// --insecure-storage, new in that version, makes glab write the token to
// its config file.
func LoginAdvice(host string) string {
	return "`glab auth login --hostname " + host + "` (with --insecure-storage if glab is 1.111.0 or later: it then writes the token to its config file, in clear, instead of the keyring, which glcli does not read)"
}

// noTokenInFile is the error for a host that glab's config has, without a
// token: the token is most likely in the keyring of the operating system,
// which glcli does not read.
//
// Since glab 1.111.0, `glab auth login` stores the token there whenever
// there is a keyring (not in CI), and leaves "use_keyring: true" and no
// token in the file; --insecure-storage, new in that version, makes it
// write the token to the file as before. From 1.84.0 to 1.110, the same
// happened with --use-keyring only. Before 1.84.0, --use-keyring wrote no
// "use_keyring" to the file. A host glab logged out of can be there
// without a token too.
func (c *Config) noTokenInFile(h Host) error {
	why := "the host is there without a token: glab is logged out of it, or keeps its token in the keyring of the operating system, which glcli does not read"
	login := "run " + LoginAdvice(h.Name)
	if h.Keyring {
		why = "glab keeps it in the keyring of the operating system (use_keyring is true for this host), which glcli does not read"
		login = "run `glab auth login --hostname " + h.Name + " --insecure-storage` (glab 1.111.0 or later), which makes glab write the token to that file, in clear"
	}
	env := "set GLCLI_TOKEN to a personal access token"
	if h.Name != c.Default() {
		env = "set GLCLI_HOST=" + h.Name + " and GLCLI_TOKEN to a personal access token (GLCLI_TOKEN is only used for the default host, which is " + c.Default() + ")"
	}
	return fmt.Errorf("no token for %s in %s: %s. Either %s, or %s", h.Name, c.Path, why, login, env)
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
