// Copyright 2026 Jonas Kaninda
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.

package njia

import (
	"fmt"
	"net/http"
	"strings"
)

// hostKind classifies a host pattern. The values are ordered by increasing
// specificity, which is what decides between two routes registered under the
// same path pattern.
type hostKind uint8

const (
	// hostAny matches every host. It is what "*" and an unconstrained route
	// both compile to.
	hostAny hostKind = iota
	// hostSuffixWild matches one or more leading labels followed by a fixed
	// suffix: "*.example.com" and "{sub...}.example.com".
	hostSuffixWild
	// hostLabel matches exactly one leading label followed by a fixed suffix:
	// "{sub}.example.com".
	hostLabel
	// hostExact matches one literal authority.
	hostExact
)

// hostPattern is a compiled host constraint.
//
// Matching is case-insensitive, as host names are, and allocation-free: a host
// that is already lower case — which is the overwhelmingly common case, since
// clients send what the URL contained — is compared without being copied.
type hostPattern struct {
	// raw is the pattern as written, for introspection and error messages.
	raw string
	// kind classifies the pattern.
	kind hostKind
	// name is the parameter name a capturing pattern binds, or empty.
	name string
	// literal is the lower-cased whole host for hostExact.
	literal string
	// suffix is the lower-cased fixed part, including its leading dot, for
	// hostSuffixWild and hostLabel.
	suffix string
	// port is the lower-cased port the pattern requires, or empty when the
	// pattern says nothing about the port and any port is accepted.
	port string
	// score orders patterns by specificity.
	score uint64
}

// AnyHost is the host pattern that matches every request. Writing it out is
// equivalent to registering a route with no host constraint at all, and it
// exists so that a configuration file can express "any host" explicitly.
const AnyHost = "*"

// parseHostPattern compiles a host pattern.
//
// Accepted forms, in decreasing specificity:
//
//	api.example.com          exactly this host
//	api.example.com:8443     exactly this host on this port
//	{sub}.example.com        exactly one leading label, captured as "sub"
//	*.example.com            one or more leading labels
//	{sub...}.example.com     one or more leading labels, captured as "sub"
//	{host...}                any host, captured whole
//	*                        any host
//
// A wildcard or placeholder is only legal as the leading label. A pattern may
// end in ":port"; when it does, the request's port must match, and when it does
// not, the request's port is ignored.
func parseHostPattern(raw string) (*hostPattern, error) {
	if raw == "" {
		return nil, fmt.Errorf("%w: pattern is empty", ErrBadHost)
	}
	if strings.ContainsAny(raw, "/ \t") {
		return nil, fmt.Errorf("%w: %q contains a path or whitespace", ErrBadHost, raw)
	}

	body, port, err := splitPatternPort(raw)
	if err != nil {
		return nil, err
	}
	p := &hostPattern{raw: raw, port: lower(port)}

	switch {
	case body == AnyHost:
		p.kind = hostAny

	case strings.HasPrefix(body, "*."):
		suffix := body[1:] // keep the dot
		if err := checkLiteralHost(raw, suffix[1:]); err != nil {
			return nil, err
		}
		p.kind = hostSuffixWild
		p.suffix = lower(suffix)

	case strings.HasPrefix(body, "{"):
		close := strings.IndexByte(body, '}')
		if close < 0 {
			return nil, fmt.Errorf("%w: unbalanced braces in %q", ErrBadHost, raw)
		}
		name := body[1:close]
		rest := body[close+1:]
		catchAll := strings.HasSuffix(name, "...")
		if catchAll {
			name = name[:len(name)-3]
		}
		if name == "" {
			return nil, fmt.Errorf("%w: empty placeholder name in %q", ErrBadHost, raw)
		}
		if strings.ContainsAny(name, "{}.*:") {
			return nil, fmt.Errorf("%w: invalid placeholder name %q in %q", ErrBadHost, name, raw)
		}
		p.name = name
		switch {
		case rest == "":
			if !catchAll {
				// "{sub}" alone would mean a single-label host, which is never
				// a real request host. Requiring the catch-all form makes the
				// intent explicit.
				return nil, fmt.Errorf("%w: %q must be written {%s...} to match a whole host", ErrBadHost, raw, name)
			}
			p.kind = hostAny
		case rest[0] != '.':
			return nil, fmt.Errorf("%w: %q must place the placeholder in its own leading label", ErrBadHost, raw)
		default:
			if err := checkLiteralHost(raw, rest[1:]); err != nil {
				return nil, err
			}
			p.suffix = lower(rest)
			if catchAll {
				p.kind = hostSuffixWild
			} else {
				p.kind = hostLabel
			}
		}

	default:
		if strings.ContainsAny(body, "*{}") {
			return nil, fmt.Errorf("%w: %q may only use a wildcard in its leading label", ErrBadHost, raw)
		}
		if err := checkLiteralHost(raw, body); err != nil {
			return nil, err
		}
		p.kind = hostExact
		p.literal = lower(body)
	}

	// Specificity: kind first, then a port-qualified pattern over a bare one,
	// then the longer fixed part.
	fixed := len(p.literal) + len(p.suffix)
	if fixed > 0xFFFF {
		fixed = 0xFFFF
	}
	p.score = uint64(p.kind) << 40
	if p.port != "" {
		p.score |= 1 << 39
	}
	p.score |= uint64(fixed) << 16
	return p, nil
}

// checkLiteralHost rejects a fixed host part that is not a plausible host name.
func checkLiteralHost(raw, s string) error {
	if s == "" {
		return fmt.Errorf("%w: %q has an empty host part", ErrBadHost, raw)
	}
	if strings.ContainsAny(s, "*{}") {
		return fmt.Errorf("%w: %q may only use a wildcard in its leading label", ErrBadHost, raw)
	}
	if strings.HasPrefix(s, ".") || strings.Contains(s, "..") {
		return fmt.Errorf("%w: %q has an empty label", ErrBadHost, raw)
	}
	return nil
}

// splitPatternPort separates a trailing ":port" from a host pattern, leaving a
// bracketed IPv6 literal intact.
func splitPatternPort(raw string) (host, port string, err error) {
	if strings.HasPrefix(raw, "[") {
		end := strings.IndexByte(raw, ']')
		if end < 0 {
			return "", "", fmt.Errorf("%w: unbalanced brackets in %q", ErrBadHost, raw)
		}
		rest := raw[end+1:]
		switch {
		case rest == "":
			return raw, "", nil
		case rest[0] == ':':
			return raw[:end+1], rest[1:], nil
		default:
			return "", "", fmt.Errorf("%w: trailing %q after the address in %q", ErrBadHost, rest, raw)
		}
	}
	i := strings.LastIndexByte(raw, ':')
	if i < 0 {
		return raw, "", nil
	}
	if strings.IndexByte(raw, ':') != i {
		// More than one colon and no brackets: an unbracketed IPv6 literal,
		// which is not a legal Host header value.
		return "", "", fmt.Errorf("%w: %q has several colons; bracket an IPv6 address", ErrBadHost, raw)
	}
	if i == len(raw)-1 {
		return "", "", fmt.Errorf("%w: %q ends in a colon", ErrBadHost, raw)
	}
	return raw[:i], raw[i+1:], nil
}

// key returns the map key an exact pattern is indexed under, and whether the
// pattern can be indexed at all.
//
// Only a port-agnostic exact host is indexed. A pattern that also fixes a port
// would need the request's authority rebuilt as "host:port" to be looked up,
// which would allocate on every request; those are rare, so they are kept in a
// small ordered list and matched directly instead.
func (p *hostPattern) key() (string, bool) {
	if p.kind != hostExact || p.port != "" {
		return "", false
	}
	return p.literal, true
}

// match reports whether the pattern accepts the authority, and appends the
// captured parameter, if the pattern binds one, to dst.
//
// host is the authority with any port and trailing dot already removed;
// port is what was removed, without its colon.
func (p *hostPattern) match(host, port string, dst []PathParam) ([]PathParam, bool) {
	if p.port != "" && !equalFold(port, p.port) {
		return dst, false
	}
	switch p.kind {
	case hostAny:
		if p.name != "" {
			return append(dst, PathParam{Name: p.name, Value: host}), true
		}
		return dst, true

	case hostExact:
		return dst, equalFold(host, p.literal)

	case hostLabel:
		prefix, ok := trimHostSuffix(host, p.suffix)
		if !ok || prefix == "" || strings.IndexByte(prefix, '.') >= 0 {
			return dst, false
		}
		if p.name != "" {
			return append(dst, PathParam{Name: p.name, Value: prefix}), true
		}
		return dst, true

	case hostSuffixWild:
		prefix, ok := trimHostSuffix(host, p.suffix)
		if !ok || prefix == "" {
			return dst, false
		}
		if p.name != "" {
			return append(dst, PathParam{Name: p.name, Value: prefix}), true
		}
		return dst, true
	}
	return dst, false
}

// trimHostSuffix removes suffix from host, case-insensitively, and returns what
// was in front of it.
func trimHostSuffix(host, suffix string) (string, bool) {
	if len(host) <= len(suffix) {
		return "", false
	}
	if !equalFold(host[len(host)-len(suffix):], suffix) {
		return "", false
	}
	return host[:len(host)-len(suffix)], true
}

// requestAuthority splits the host a request should be matched against into its
// name and port, dropping a trailing dot. Nothing is allocated: every result is
// a slice of the request's own string.
func requestAuthority(req *http.Request) (host, port string) {
	authority := req.Host
	if req.URL != nil && req.URL.Host != "" {
		authority = req.URL.Host
	}
	host, port = splitAuthority(authority)
	// "example.com." and "example.com" are the same name.
	if len(host) > 1 && host[len(host)-1] == '.' {
		host = host[:len(host)-1]
	}
	return host, port
}

// splitAuthority separates "host:port", leaving a bracketed IPv6 literal
// intact and tolerating malformed input rather than failing.
func splitAuthority(authority string) (host, port string) {
	if strings.HasPrefix(authority, "[") {
		if end := strings.IndexByte(authority, ']'); end >= 0 {
			if rest := authority[end+1:]; len(rest) > 1 && rest[0] == ':' {
				return authority[:end+1], rest[1:]
			}
			return authority[:end+1], ""
		}
		return authority, ""
	}
	i := strings.LastIndexByte(authority, ':')
	if i < 0 || strings.IndexByte(authority, ':') != i {
		// No colon, or several without brackets: treat the whole thing as the
		// name rather than guessing.
		return authority, ""
	}
	return authority[:i], authority[i+1:]
}

// equalFold compares two ASCII strings case-insensitively without allocating.
// Host names are ASCII by the time they reach a router: an internationalised
// name arrives punycode-encoded.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca == cb {
			continue
		}
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// lower returns s in lower case, returning s itself when it already is.
func lower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for ; i < len(b); i++ {
				if c := b[i]; c >= 'A' && c <= 'Z' {
					b[i] = c + 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// hasUpper reports whether s contains an ASCII upper-case letter.
func hasUpper(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			return true
		}
	}
	return false
}

// id returns a string that is equal for two patterns expressing the same
// constraint, so that registering the same host twice reuses one variant.
func (p *hostPattern) id() string {
	return string('0'+byte(p.kind)) + "\x00" + p.name + "\x00" + p.literal + "\x00" + p.suffix + "\x00" + p.port
}

// ValidateHost reports whether a host pattern is well formed, without
// registering anything. A gateway that builds its route table from user
// configuration can use it to reject a bad entry early, with the same error it
// would get from registration.
//
// Accepted forms, in decreasing specificity:
//
//	api.example.com          exactly this host
//	api.example.com:8443     exactly this host on this port
//	{sub}.example.com        exactly one leading label, captured as "sub"
//	*.example.com            one or more leading labels
//	{sub...}.example.com     one or more leading labels, captured as "sub"
//	{host...}                any host, captured whole
//	*                        any host
//
// Matching is case-insensitive and ignores a trailing dot. A pattern that names
// a port only matches requests carrying that port; a pattern that does not
// accepts any port.
func ValidateHost(pattern string) error {
	_, err := parseHostPattern(pattern)
	return err
}

// compileHosts parses the route's host patterns and reports the parameter name
// they capture, if any.
func (r *Route) compileHosts() (string, error) {
	if len(r.hosts) == 0 {
		return "", nil
	}
	pats := make([]*hostPattern, 0, len(r.hosts))
	seen := make(map[string]struct{}, len(r.hosts))
	param := ""
	for _, raw := range r.hosts {
		p, err := parseHostPattern(raw)
		if err != nil {
			return "", err
		}

		if _, dup := seen[p.id()]; dup {
			continue
		}
		seen[p.id()] = struct{}{}
		if p.name != "" {
			if param != "" && param != p.name {
				return "", fmt.Errorf("%w: host patterns of one route capture different names, %q and %q",
					ErrParamConflict, param, p.name)
			}
			param = p.name
		}
		pats = append(pats, p)
	}
	if param != "" {
		// If one pattern captured and another did not, the parameter would be
		// present for some requests and absent for others.
		for _, p := range pats {
			if p.name == "" {
				return "", fmt.Errorf("%w: host pattern %q captures nothing while %q captures %q",
					ErrParamConflict, p.raw, pats[0].raw, param)
			}
		}
	}
	r.hostPats = pats
	return param, nil
}
