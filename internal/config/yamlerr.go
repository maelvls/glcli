package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// The yaml package quotes the file in some of its errors ("cannot unmarshal
// !!str `glpat-…` into …", "unknown anchor '…' referenced", "mapping key
// "…" already defined"), and the file holds tokens. So none of its messages
// is passed on as it is: diagnose says what is wrong from the nodes, and
// redact keeps a message only when it is one of yamlReasons.

// yamlReasons are the messages of yaml.v3 (v3.0.1) that are fixed texts,
// with nothing of the file in them: every "problem" its scanner, parser and
// reader can set, and the fixed failf calls of decode.go. A message is kept
// only when it is exactly one of these.
var yamlReasons = map[string]bool{
	// scannerc.go
	"block sequence entries are not allowed in this context":       true,
	"could not find expected ':'":                                  true,
	"could not find expected directive name":                       true,
	"did not find expected '!'":                                    true,
	"did not find expected alphabetic or numeric character":        true,
	"did not find expected comment or line break":                  true,
	"did not find expected digit or '.' character":                 true,
	"did not find expected hexdecimal number":                      true,
	"did not find expected tag URI":                                true,
	"did not find expected version number":                         true,
	"did not find expected whitespace or line break":               true,
	"did not find expected whitespace":                             true,
	"did not find the expected '>'":                                true,
	"did not find URI escaped octet":                               true,
	"found a tab character that violates indentation":              true,
	"found a tab character where an indentation space is expected": true,
	"found an incorrect leading UTF-8 octet":                       true,
	"found an incorrect trailing UTF-8 octet":                      true,
	"found an indentation indicator equal to 0":                    true,
	"found character that cannot start any token":                  true,
	"found extremely long version number":                          true,
	"found invalid Unicode character escape code":                  true,
	"found unexpected document indicator":                          true,
	"found unexpected end of stream":                               true,
	"found unexpected non-alphabetical character":                  true,
	"found unknown directive name":                                 true,
	"found unknown escape character":                               true,
	"mapping keys are not allowed in this context":                 true,
	"mapping values are not allowed in this context":               true,
	// scannerc.go, "exceeded max depth of %d" with its two constants.
	"exceeded max depth of 10000": true,
	// parserc.go
	"did not find expected '-' indicator":    true,
	"did not find expected ',' or ']'":       true,
	"did not find expected ',' or '}'":       true,
	"did not find expected <document start>": true,
	"did not find expected <stream-start>":   true,
	"did not find expected key":              true,
	"did not find expected node content":     true,
	"found duplicate %TAG directive":         true,
	"found duplicate %YAML directive":        true,
	"found incompatible YAML document":       true,
	"found undefined tag handle":             true,
	// readerc.go
	"control characters are not allowed": true,
	"expected low surrogate area":        true,
	"incomplete UTF-16 character":        true,
	"incomplete UTF-16 surrogate pair":   true,
	"incomplete UTF-8 octet sequence":    true,
	"invalid leading UTF-8 octet":        true,
	"invalid length of a UTF-8 sequence": true,
	"invalid trailing UTF-8 octet":       true,
	"invalid Unicode character":          true,
	"unexpected low surrogate area":      true,
	// decode.go
	"unknown problem parsing YAML content":                     true,
	"attempted to go past the end of stream; corrupted value?": true,
	"document contains excessive aliasing":                     true,
	"!!binary value contains invalid base64 data":              true,
	"map merge requires map or sequence of maps as the value":  true,
}

// yamlLine is how the yaml package starts a message it has a line for.
var yamlLine = regexp.MustCompile(`^line (\d+): `)

// redact turns an error of the yaml package into a message with nothing of
// the file in it: the lines the error points at, and its reason when it is
// one of yamlReasons. "near", because for a syntax error the line can be
// the one where the enclosing block starts.
func redact(err error) string {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		// What diagnose could not explain: these messages quote the file,
		// only their lines are kept.
		var lines []string
		for _, msg := range te.Errors {
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
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	out := "invalid YAML"
	if m := yamlLine.FindStringSubmatch(msg); m != nil {
		out += " near line " + m[1]
		msg = msg[len(m[0]):]
	}
	if yamlReasons[msg] {
		out += ": " + msg
	}
	return out
}

// diagnose says what in the shape of the file the yaml package refused: a
// value that is not a mapping where one is needed, a key given twice, a key
// that is not a string. It names the first one of the file, by its line and
// its kind only, and returns "" when it finds none (merge keys are not
// followed).
func diagnose(doc *yaml.Node) string {
	var first *yaml.Node
	var what string
	report := func(n *yaml.Node, msg string) {
		if first == nil || before(n, first) {
			first, what = n, msg
		}
	}
	// mapping checks that n is a mapping (or nothing) with sound keys, and
	// returns its keys and values.
	mapping := func(n *yaml.Node, name string) []*yaml.Node {
		m := target(n)
		if isNull(m) {
			return nil
		}
		if m.Kind != yaml.MappingNode {
			report(n, name+" is not a mapping")
			return nil
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			k := m.Content[i]
			if target(k).Kind != yaml.ScalarNode {
				report(k, "a key is not a string")
			}
			for j := 0; j < i; j += 2 {
				// The yaml package compares the keys this way.
				if prev := m.Content[j]; prev.Kind == k.Kind && prev.Value == k.Value {
					report(k, fmt.Sprintf("duplicate key (first at line %d)", prev.Line))
					break
				}
			}
		}
		return m.Content
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return ""
	}
	root := mapping(doc.Content[0], "the file")
	for i := 0; i+1 < len(root); i += 2 {
		if root[i].Kind != yaml.ScalarNode || root[i].Value != "hosts" {
			continue
		}
		hosts := mapping(root[i+1], "hosts")
		for j := 0; j+1 < len(hosts); j += 2 {
			if hosts[j].Tag != "!!merge" {
				mapping(hosts[j+1], "a host")
			}
		}
	}
	if first == nil {
		return ""
	}
	return fmt.Sprintf("line %d: %s", first.Line, what)
}
