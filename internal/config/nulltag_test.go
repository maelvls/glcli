package config

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Long on purpose: the yaml package cuts the values it quotes after seven
// characters ("glpat-S..."), so that looking for the end of a token in an
// error finds nothing.
const fakeToken = "glpat-SECRET0123456789"

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

// No tag changes what the token is.
func TestParseTokenAnyTag(t *testing.T) {
	clearEnv(t)
	for _, tag := range []string{"", "!!null ", "!!str ", "!!int ", "!!bool ", "!!float ", "!!timestamp ", "!mine ", "! "} {
		c, err := Parse([]byte("hosts:\n    gitlab.com:\n        token: "+tag+fakeToken+"\n"), "/x/config.yml")
		if err != nil {
			t.Fatalf("token: %s…: %v", tag, err)
		}
		if h, err := c.Host("gitlab.com"); err != nil || h.Token != fakeToken {
			t.Fatalf("token: %s…: %+v %v", tag, h, err)
		}
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

// A file with nothing in it is a config without hosts.
func TestParseEmpty(t *testing.T) {
	clearEnv(t)
	for _, data := range []string{"", "\n", "# nothing\n", "~\n", "---\n", "hosts:\n", "host:\nhosts: {}\n"} {
		c, err := Parse([]byte(data), "/x/config.yml")
		if err != nil || c.DefaultHost != "gitlab.com" || len(c.Hosts) != 0 {
			t.Errorf("%q: %+v %v", data, c, err)
		}
	}
}

// errorShape is every error Parse can return: the path, lines, one of our
// own texts or a fixed text of the yaml package. Nothing of the file but
// line numbers fits in it.
var errorShape = func() *regexp.Regexp {
	var reasons []string
	for r := range yamlReasons {
		reasons = append(reasons, regexp.QuoteMeta(r))
	}
	slices.Sort(reasons)
	return regexp.MustCompile(`^reading /x/config\.yml: (` +
		`invalid YAML( near line \d+)?(: (` + strings.Join(reasons, "|") + `))?` +
		`|invalid YAML near lines \d+(, \d+)+` +
		`|line \d+: (host|api_host|api_protocol|token|user) is not a string` +
		`|line \d+: (the file|hosts|a host) is not a mapping` +
		`|line \d+: a key is not a string` +
		`|line \d+: duplicate key \(first at line \d+\)` +
		`)$`)
}()

func checkError(t *testing.T, data []byte, err error) {
	t.Helper()
	if err == nil {
		return
	}
	msg := err.Error()
	if !errorShape.MatchString(msg) {
		t.Errorf("%q: unexpected error, it may repeat the file: %s", data, msg)
	}
	// "glpat-S" is what is left of a token the yaml package quotes.
	if strings.Contains(msg, "glpat") || strings.Contains(msg, "SECRET") {
		t.Errorf("%q: the error leaks the content of the file: %s", data, msg)
	}
}

// What is wrong with each file, and what Parse says of it: the file, the
// line the yaml package gives (line 1 for the bad indentation of line 4)
// and the kind of problem, never what the file holds.
var badConfigs = []struct{ name, data, want string }{
	// Not YAML: the reason is the yaml package's, when it is a fixed text.
	{"bad indentation", "hosts:\n    gitlab.com:\n        token: " + fakeToken + "\n      user: me\n", "invalid YAML near line 1: did not find expected key"},
	{"bad indentation, CRLF", "hosts:\r\n    gitlab.com:\r\n        token: " + fakeToken + "\r\n      user: me\r\n", "invalid YAML near line 1: did not find expected key"},
	{"unclosed quote", "hosts:\n    gitlab.com:\n        token: \"" + fakeToken + "\n", "invalid YAML near line 3: found unexpected end of stream"},
	{"unknown escape", "hosts:\n    gitlab.com:\n        token: \"\\q" + fakeToken + "\"\n", "invalid YAML near line 3: found unknown escape character"},
	{"unclosed tag", "hosts:\n    gitlab.com:\n        token: !<" + fakeToken + "\n", "invalid YAML near line 3: did not find the expected '>'"},
	{"tabs", "hosts:\n\tgitlab.com:\n\t\ttoken: " + fakeToken + "\n", "invalid YAML near line 2: found character that cannot start any token"},
	{"tab after spaces", "hosts:\n    gitlab.com:\n        token: " + fakeToken + "\n\tuser: me\n", "invalid YAML near line 3: found a tab character that violates indentation"},
	{"stray brace", "hosts:\n    gitlab.com:\n        token: " + fakeToken + "\n    }\n", "invalid YAML near line 1: did not find expected key"},
	{"unclosed mapping", "hosts: {gitlab.com: {token: " + fakeToken + "}\n", "invalid YAML near line 1: did not find expected ',' or '}'"},
	{"unclosed list", "hosts: [" + fakeToken + "\n", "invalid YAML near line 1: did not find expected ',' or ']'"},
	{"control character", "hosts:\n    gitlab.com:\n        token: " + fakeToken + "\x01\n", "invalid YAML: control characters are not allowed"},
	{"not UTF-8", "hosts:\n    gitlab.com:\n        token: " + fakeToken + "\xff\n", "invalid YAML: invalid leading UTF-8 octet"},
	{"cut UTF-16", "\xff\xfeh\x00o\x00s\x00t\x00s\x00:\x00 \x00[\x00\n", "invalid YAML: incomplete UTF-16 character"},
	{"YAML version", "%YAML 9.9\n---\nhosts: " + fakeToken + "\n", "invalid YAML: found incompatible YAML document"},
	{"merge of a text", "hosts:\n    <<: " + fakeToken + "\n", "invalid YAML: map merge requires map or sequence of maps as the value"},
	{"binary key", "hosts:\n    !!binary " + fakeToken + "!:\n        user: me\n", "invalid YAML: !!binary value contains invalid base64 data"},
	// The yaml package quotes the file in these: nothing of its message is kept.
	{"unknown anchor", "hosts:\n    gitlab.com:\n        token: *" + fakeToken + "\n", "invalid YAML"},
	{"anchor in itself", "hosts: &" + fakeToken + "\n    <<: *" + fakeToken + "\n", "invalid YAML"},
	{"null key", "hosts:\n    !!null " + fakeToken + ":\n        user: me\n", "invalid YAML"},
	{"merged host", "x: &m\n    a.example.com: " + fakeToken + "\nhosts:\n    <<: *m\n", "invalid YAML near line 2"},
	{"merged hosts", "x: &m\n    a.example.com: " + fakeToken + "\n    b.example.com: " + fakeToken + "\nhosts:\n    <<: *m\n", "invalid YAML near lines 2, 3"},
	// YAML, but not a config.
	{"token as file", fakeToken + "\n", "line 1: the file is not a mapping"},
	{"tagged token as file", "!!null " + fakeToken + "\n", "line 1: the file is not a mapping"},
	{"list as file", "- " + fakeToken + "\n", "line 1: the file is not a mapping"},
	{"token as hosts", "hosts: " + fakeToken + "\n", "line 1: hosts is not a mapping"},
	{"token as hosts, UTF-16", "\xff\xfeh\x00o\x00s\x00t\x00s\x00:\x00 \x00g\x00l\x00p\x00a\x00t\x00-\x00S\x00\n\x00", "line 1: hosts is not a mapping"},
	{"tagged token as hosts", "hosts: !!null " + fakeToken + "\n", "line 1: hosts is not a mapping"},
	{"list as hosts", "hosts: [" + fakeToken + "]\n", "line 1: hosts is not a mapping"},
	{"token as host", "hosts:\n    gitlab.com: " + fakeToken + "\n", "line 2: a host is not a mapping"},
	{"token as host, CRLF", "hosts:\r\n    gitlab.com: " + fakeToken + "\r\n", "line 2: a host is not a mapping"},
	{"token as host, BOM", "\xef\xbb\xbfhosts:\n    gitlab.com: " + fakeToken + "\n", "line 2: a host is not a mapping"},
	{"tagged token as host", "hosts:\n    gitlab.com: !!null " + fakeToken + "\n", "line 2: a host is not a mapping"},
	{"list as host", "hosts:\n    gitlab.com:\n        - token: " + fakeToken + "\n", "line 3: a host is not a mapping"},
	{"list as key", "hosts:\n    ? [" + fakeToken + "]\n    : {token: " + fakeToken + "}\n", "line 2: a key is not a string"},
	{"duplicate token", "hosts:\n    gitlab.com:\n        token: !!null " + fakeToken + "\n        token: " + fakeToken + "\n", "line 4: duplicate key (first at line 3)"},
	{"duplicate host", "hosts:\n    " + fakeToken + ":\n        user: a\n    " + fakeToken + ":\n        user: b\n", "line 4: duplicate key (first at line 2)"},
	{"duplicate top-level key", "host: a\nhosts: {}\nhost: " + fakeToken + "\n", "line 3: duplicate key (first at line 1)"},
	{"duplicate key, no hosts", "hosts:\nhost: a\nhost: " + fakeToken + "\n", "line 3: duplicate key (first at line 2)"},
	{"list as token", "hosts:\n    gitlab.com:\n        token: [" + fakeToken + "]\n", "line 3: token is not a string"},
	{"mapping as token", "hosts:\n    gitlab.com:\n        token:\n            " + fakeToken + ": " + fakeToken + "\n", "line 4: token is not a string"},
	{"list as default host", "host: [" + fakeToken + "]\n", "line 1: host is not a string"},
	{"list as merged token", "x: &d\n    token: [" + fakeToken + "]\nhosts:\n    gitlab.com:\n        <<: *d\n", "line 2: token is not a string"},
	{"two bad fields", "hosts:\n    b.example.com:\n        token: [" + fakeToken + "]\n    a.example.com:\n        user: [" + fakeToken + "]\n", "line 3: token is not a string"},
}

func TestParseErrors(t *testing.T) {
	clearEnv(t)
	for _, tc := range badConfigs {
		c, err := Parse([]byte(tc.data), "/x/config.yml")
		if err == nil {
			t.Errorf("%s: expected an error, got %+v", tc.name, c)
			continue
		}
		if got, want := err.Error(), "reading /x/config.yml: "+tc.want; got != want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, want)
		}
		checkError(t, []byte(tc.data), err)
	}
}

// The hosts are visited in no fixed order: the field named must still be
// the same one each time, the first of the file, even on a single line.
func TestParseErrorIsStable(t *testing.T) {
	clearEnv(t)
	for range 200 {
		_, err := Parse([]byte("hosts: {a: {token: [x]}, b: {user: [y]}, c: {api_host: [z]}}\n"), "/x/config.yml")
		if err == nil || err.Error() != "reading /x/config.yml: line 1: token is not a string" {
			t.Fatalf("got %v", err)
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

// And so is a merge key, with a tagged token on either side of it.
func TestParseMergeKey(t *testing.T) {
	clearEnv(t)
	c, err := Parse([]byte(`
defaults: &d
    api_protocol: http
    user: me
    token: !!null glpat-merged
hosts:
    a.example.com:
        <<: *d
    b.example.com:
        <<: *d
        user: other
        token: !!null glpat-own
`), "/x/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	if h, err := c.Host("a.example.com"); err != nil || h.Token != "glpat-merged" || h.User != "me" || h.BaseURL() != "http://a.example.com" {
		t.Fatalf("a.example.com: %+v %v", h, err)
	}
	if h, err := c.Host("b.example.com"); err != nil || h.Token != "glpat-own" || h.User != "other" || h.BaseURL() != "http://b.example.com" {
		t.Fatalf("b.example.com: %+v %v", h, err)
	}
}

const taggedSample = `
host: gitlab.com
hosts:
    gitlab.com:
        api_host: gitlab.com
        api_protocol: https
        user: me
        token: !!null glpat-aaa
    code.pan.run:
        token: glpat-bbb
        user: other
`

func checkSample(t *testing.T, name string, data string) {
	t.Helper()
	c, err := Parse([]byte(data), "/x/config.yml")
	if err != nil {
		t.Errorf("%s: %v", name, err)
		return
	}
	if got := strings.Join(c.Names(), " "); got != "code.pan.run gitlab.com" || c.DefaultHost != "gitlab.com" {
		t.Errorf("%s: hosts %q, default %q", name, got, c.DefaultHost)
	}
	if h := c.Hosts["gitlab.com"]; h.Token != "glpat-aaa" || h.User != "me" || h.BaseURL() != "https://gitlab.com" || h.TokenSource != "/x/config.yml" {
		t.Errorf("%s: gitlab.com: %+v", name, h)
	}
	if h := c.Hosts["code.pan.run"]; h.Token != "glpat-bbb" || h.User != "other" || h.BaseURL() != "https://code.pan.run" {
		t.Errorf("%s: code.pan.run: %+v", name, h)
	}
}

// The sample of the other tests, and the same file as an editor on Windows
// would save it: CRLF line ends, a byte order mark.
func TestParseSampleStillParses(t *testing.T) {
	clearEnv(t)
	for name, data := range map[string]string{"plain": sample, "tagged": taggedSample} {
		crlf := strings.ReplaceAll(data, "\n", "\r\n")
		checkSample(t, name, data)
		checkSample(t, name+", CRLF", crlf)
		checkSample(t, name+", BOM", "\xef\xbb\xbf"+data)
		checkSample(t, name+", BOM and CRLF", "\xef\xbb\xbf"+crlf)
	}
}

// FuzzParse feeds Parse broken configs: it must not panic, and its errors
// must keep to errorShape.
func FuzzParse(f *testing.F) {
	for _, tc := range badConfigs {
		f.Add([]byte(tc.data))
	}
	f.Add([]byte(sample))
	f.Add([]byte(taggedSample))
	f.Add([]byte("x: &d\n    token: !!null " + fakeToken + "\nhosts:\n    gitlab.com:\n        <<: *d\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, err := Parse(data, "/x/config.yml")
		checkError(t, data, err)
	})
}
