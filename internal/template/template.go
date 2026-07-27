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

// Package template parses route templates of the form "/users/{id}" and
// "/users/{id:[0-9]+}" into a compiled regular expression, an ordered list of
// variable names, and a reverse-building template used for URL construction.
//
// The package is shared by the native njia router and by the muxcompat
// package. It is intentionally free of any HTTP types.
package template

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Kind identifies which part of a request a template is matched against.
// The kind selects the implicit pattern used for a bare {name} variable and
// controls anchoring.
type Kind int

const (
	// KindPath matches against the request path and is anchored at both ends.
	KindPath Kind = iota
	// KindPathPrefix matches against a leading portion of the request path and
	// is anchored only at the start.
	KindPathPrefix
	// KindHost matches against the request host and is anchored at both ends.
	KindHost
	// KindQuery matches against a single "key=value" pair extracted from the
	// raw query string and is anchored at both ends.
	KindQuery
)

// Options carries the router-level flags that influence how a template is
// compiled.
type Options struct {
	// StrictSlash makes a compiled path pattern tolerate an optional trailing
	// slash so that the router can observe the mismatch and redirect.
	StrictSlash bool
	// UseEncodedPath indicates the template will be matched against
	// URL.EscapedPath rather than URL.Path. It does not change compilation but
	// is recorded so callers can consult it.
	UseEncodedPath bool
	// RejectDuplicateNames makes Parse fail when a template declares the same
	// variable name twice. The native njia router enables this; muxcompat does
	// not, because gorilla/mux accepts such templates and muxcompat must match
	// gorilla's observable behavior.
	RejectDuplicateNames bool
}

// Errors returned by Parse.
var (
	// ErrUnbalancedBraces reports a template whose braces do not nest and close
	// correctly, such as "/{id" or "/}id{".
	ErrUnbalancedBraces = errors.New("njia/template: unbalanced braces")
	// ErrMissingName reports a variable with an empty name, such as "/{}".
	ErrMissingName = errors.New("njia/template: missing variable name")
	// ErrMissingPattern reports a variable with an empty pattern, such as
	// "/{id:}".
	ErrMissingPattern = errors.New("njia/template: missing variable pattern")
	// ErrDuplicateName reports a template that declares the same variable name
	// more than once.
	ErrDuplicateName = errors.New("njia/template: duplicate variable name")
	// ErrBadQueryTemplate reports a query template that is not of the form
	// "key=value".
	ErrBadQueryTemplate = errors.New("njia/template: query template must be key=value")
	// ErrCapturingGroup reports a variable pattern that contains a capturing
	// group. Capture groups would shift the submatch indices the extractor
	// relies on. Callers that must reproduce gorilla's behavior turn this into
	// a panic; the native router surfaces it as an ordinary error.
	ErrCapturingGroup = errors.New("njia/template: capturing group in variable pattern")
)

// Var describes a single {name} or {name:pattern} placeholder in a template.
type Var struct {
	// Name is the variable name as written in the template.
	Name string
	// Pattern is the regular expression source the variable matches. For a bare
	// {name} it is the kind's default pattern.
	Pattern string
	// Explicit reports whether the pattern was written in the template rather
	// than defaulted.
	Explicit bool
	// Index is the position of the variable within the template, starting at 0.
	Index int
}

// Template is a compiled route template.
type Template struct {
	// Raw is the template exactly as supplied by the caller.
	Raw string
	// Kind is the part of the request this template matches.
	Kind Kind
	// Source is the anchored regular expression source the template compiles
	// to. It is always available; the compiled form may not be yet.
	Source string
	// Vars lists the template's variables in the order they appear.
	Vars []Var
	// Reverse is the template with every placeholder replaced by "%s", suitable
	// for use with fmt.Sprintf when building a URL.
	Reverse string
	// VarRegexps holds one anchored regexp per variable, used to validate
	// values supplied to reverse building.
	VarRegexps []*regexp.Regexp
	// Options records the options the template was compiled with.
	Options Options
	// Static is the literal prefix of the template up to the first variable.
	// For a template with no variables it is the whole template.
	Static string
	// NoVars reports whether the template contains no placeholders at all.
	NoVars bool
	// AllDefault reports whether every variable uses the kind's default
	// pattern. Such templates can be matched by a radix tree without regexp.
	AllDefault bool
	// StrictSlash reports whether strict-slash handling was actually applied,
	// which is only the case for KindPath.
	StrictSlash bool
	// EndsInSlash reports whether Raw ended in a slash that was trimmed for
	// strict-slash compilation.
	EndsInSlash bool
	// Literal reports that matching the template is exact string comparison
	// against Static — a prefix comparison for KindPathPrefix — so no regular
	// expression is needed at all.
	Literal bool

	// re is the compiled pattern. A template with no variables cannot fail to
	// compile, so its compilation is deferred: a thousand static routes then
	// cost nothing to register, and the ones the lookup index answers directly
	// are never compiled at all.
	re   *regexp.Regexp
	once sync.Once
}

// Re returns the compiled pattern, compiling it on first use.
func (t *Template) Re() *regexp.Regexp {
	t.once.Do(func() {
		if t.re == nil {
			// Deferred compilation only happens for variable-free templates,
			// whose source is this package's own quoted output and cannot be
			// invalid.
			t.re = regexp.MustCompile(t.Source)
		}
	})
	return t.re
}

// defaultVarRes holds one compiled matcher per implicit variable pattern.
// Reusing them means a route table of plain {name} wildcards compiles no
// regular expressions of its own at registration time.
var defaultVarRes = map[string]*regexp.Regexp{
	"[^/]+": regexp.MustCompile("^[^/]+$"),
	"[^.]+": regexp.MustCompile("^[^.]+$"),
	".*":    regexp.MustCompile("^.*$"),
}

// DefaultPattern returns the implicit regular expression used for a bare
// {name} placeholder of the given kind.
func DefaultPattern(k Kind) string {
	switch k {
	case KindHost:
		return "[^.]+"
	case KindQuery:
		return ".*"
	default:
		return "[^/]+"
	}
}

// Parse compiles tpl for the given kind and options.
//
// StrictSlash is only honoured for KindPath: a trailing slash in the template
// is removed before compilation and an optional "[/]?" is appended, so that the
// compiled pattern matches with or without a trailing slash and the caller can
// compare the request against Raw to decide whether to redirect.
func Parse(tpl string, k Kind, opts Options) (*Template, error) {
	if k == KindQuery && !strings.Contains(tpl, "=") {
		return nil, fmt.Errorf("%w: %q", ErrBadQueryTemplate, tpl)
	}

	strict := opts.StrictSlash && k == KindPath
	work := tpl
	endSlash := false
	if strict && strings.HasSuffix(work, "/") {
		work = work[:len(work)-1]
		endSlash = true
	}

	idxs, err := braceIndices(work)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrUnbalancedBraces, tpl)
	}

	def := DefaultPattern(k)

	anyExplicit := false
	var (
		pattern strings.Builder
		reverse strings.Builder
		vars    []Var
		varRes  []*regexp.Regexp
		seen    = map[string]struct{}{}
	)
	pattern.WriteByte('^')

	end := 0
	for i := 0; i < len(idxs); i += 2 {
		raw := work[end:idxs[i]]
		pattern.WriteString(regexp.QuoteMeta(raw))
		reverse.WriteString(raw)

		body := work[idxs[i]+1 : idxs[i+1]-1]
		name, patt, explicit := splitVar(body, def)
		if name == "" {
			return nil, fmt.Errorf("%w: %q", ErrMissingName, tpl)
		}
		if patt == "" {
			return nil, fmt.Errorf("%w: %q", ErrMissingPattern, tpl)
		}
		if _, dup := seen[name]; dup && opts.RejectDuplicateNames {
			return nil, fmt.Errorf("%w: %q in %q", ErrDuplicateName, name, tpl)
		}
		seen[name] = struct{}{}

		grp := len(vars)
		fmt.Fprintf(&pattern, "(?P<%s>%s)", groupName(grp), patt)
		reverse.WriteString("%s")

		vr := defaultVarRes[patt]
		if explicit || vr == nil {
			var err error
			if vr, err = regexp.Compile("^" + patt + "$"); err != nil {
				return nil, fmt.Errorf("njia/template: invalid pattern %q for variable %q: %w", patt, name, err)
			}
			if vr.NumSubexp() > 0 {
				return nil, fmt.Errorf("%w: variable %q in %q; use a non-capturing group (?:...) instead", ErrCapturingGroup, name, tpl)
			}
			anyExplicit = true
		}
		varRes = append(varRes, vr)
		vars = append(vars, Var{Name: name, Pattern: patt, Explicit: explicit, Index: grp})

		end = idxs[i+1]
	}

	rest := work[end:]
	pattern.WriteString(regexp.QuoteMeta(rest))
	reverse.WriteString(rest)
	if endSlash {
		// The trailing slash was removed from the pattern so that it can be
		// optional, but reverse building must still produce the template as
		// the caller wrote it.
		reverse.WriteByte('/')
	}

	if strict {
		pattern.WriteString("[/]?")
	}
	if k == KindQuery {
		// A query template written as "key=" matches the key with any value.
		if val := strings.SplitN(work, "=", 2)[1]; val == "" {
			pattern.WriteString(def)
		}
	}
	if k != KindPathPrefix {
		pattern.WriteByte('$')
	}

	src := pattern.String()
	var re *regexp.Regexp
	if anyExplicit {
		// A template that embeds a caller-supplied pattern is compiled now, so
		// that an invalid one is reported at registration time. Templates built
		// only from this package's own quoted output and implicit wildcards
		// cannot fail, so their compilation is deferred until something
		// actually needs to match against them.
		var err error
		if re, err = regexp.Compile(src); err != nil {
			return nil, fmt.Errorf("njia/template: cannot compile %q: %w", tpl, err)
		}
	}

	t := &Template{
		Raw:         tpl,
		Kind:        k,
		Source:      src,
		re:          re,
		Vars:        vars,
		Reverse:     reverse.String(),
		VarRegexps:  varRes,
		Options:     opts,
		NoVars:      len(vars) == 0,
		AllDefault:  true,
		StrictSlash: strict,
		EndsInSlash: endSlash,
	}
	if len(idxs) > 0 {
		t.Static = work[:idxs[0]]
	} else {
		t.Static = work
	}
	for _, v := range vars {
		if v.Pattern != def {
			t.AllDefault = false
			break
		}
	}
	// The pattern is a plain anchored quotation of the template exactly when it
	// has no variables, no optional trailing slash, and no implicit query
	// value; in that case matching is string comparison.
	t.Literal = t.NoVars && !strict &&
		!(k == KindQuery && strings.SplitN(work, "=", 2)[1] == "")
	return t, nil
}

// splitVar separates a placeholder body into its name and pattern, applying
// def when no pattern is written.
func splitVar(body, def string) (name, patt string, explicit bool) {
	if i := strings.Index(body, ":"); i >= 0 {
		return body[:i], body[i+1:], true
	}
	return body, def, false
}

// groupName returns the capture group name used for the i'th variable.
// Variable names are not used directly because they may not be valid Go
// identifiers, which regexp requires for named groups.
func groupName(i int) string {
	return "v" + strconv.Itoa(i)
}

// braceIndices returns the start and end offsets of each top-level {...} group
// in s. Braces nested inside a group, as in "{id:[{]}", are treated as part of
// the group body.
func braceIndices(s string) ([]int, error) {
	var (
		level int
		start int
		idxs  []int
	)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			level++
			if level == 1 {
				start = i
			}
		case '}':
			level--
			switch {
			case level == 0:
				idxs = append(idxs, start, i+1)
			case level < 0:
				return nil, ErrUnbalancedBraces
			}
		}
	}
	if level != 0 {
		return nil, ErrUnbalancedBraces
	}
	return idxs, nil
}

// Names returns the template's variable names in order.
func (t *Template) Names() []string {
	out := make([]string, len(t.Vars))
	for i, v := range t.Vars {
		out[i] = v.Name
	}
	return out
}

// Match reports whether s satisfies the template.
func (t *Template) Match(s string) bool {
	if t.Literal {
		if t.Kind == KindPathPrefix {
			return strings.HasPrefix(s, t.Static)
		}
		return s == t.Static
	}
	return t.Re().MatchString(s)
}

// Extract appends the name/value pairs captured from s to dst. It reports
// whether s matched. Values are appended in template order.
func (t *Template) Extract(s string, dst func(name, value string)) bool {
	if t.NoVars {
		return t.Match(s)
	}
	m := t.Re().FindStringSubmatchIndex(s)
	if m == nil {
		return false
	}
	for i, v := range t.Vars {
		lo, hi := m[2*(i+1)], m[2*(i+1)+1]
		if lo < 0 {
			dst(v.Name, "")
			continue
		}
		dst(v.Name, s[lo:hi])
	}
	return true
}

// Build renders the template using values, which maps variable names to
// values. Every variable must be present and must satisfy its pattern.
func (t *Template) Build(values map[string]string) (string, error) {
	args := make([]any, len(t.Vars))
	for i, v := range t.Vars {
		val, ok := values[v.Name]
		if !ok {
			return "", fmt.Errorf("njia/template: missing value for variable %q", v.Name)
		}
		if !t.VarRegexps[i].MatchString(val) {
			return "", fmt.Errorf("njia/template: value %q for variable %q does not match %q", val, v.Name, v.Pattern)
		}
		args[i] = val
	}
	return fmt.Sprintf(t.Reverse, args...), nil
}
