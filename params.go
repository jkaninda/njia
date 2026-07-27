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
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jkaninda/njia/internal/tree"
)

// PathParam is a captured path parameter.
type PathParam struct {
	// Name is the parameter name from the route pattern.
	Name string
	// Value is the text captured from the request path.
	Value string
}

// ParamInfo describes a parameter declared by a route pattern.
type ParamInfo struct {
	// Name is the parameter name.
	Name string
	// Position is the index of the segment the parameter occupies, counting
	// from zero after the leading slash.
	Position int
	// CatchAll reports whether the parameter absorbs the remainder of the
	// path, including slashes.
	CatchAll bool
	// InHost reports that the parameter is captured from the request host
	// rather than from its path. A host parameter is always reported first and
	// has a Position of -1.
	InHost bool
}

// parsePattern converts a route pattern into tree segments and the parameter
// descriptions the introspection API reports.
//
// Patterns are whole-segment: "/users/{id}" is valid, "/files/{name}.json" is
// not. A trailing "{rest...}" absorbs the remainder of the path.
//
// A placeholder carries a name and nothing else. The ":constraint" form is
// rejected rather than ignored, so that a pattern which looks like it filters
// values never silently matches everything, and so that giving ":" a meaning
// later is an addition rather than a change of behavior.
func parsePattern(pattern string) ([]tree.Segment, []ParamInfo, error) {
	if pattern == "" || pattern[0] != '/' {
		return nil, nil, ErrNoLeadingSlash
	}
	parts := strings.Split(pattern[1:], "/")
	segs := make([]tree.Segment, 0, len(parts))
	var params []ParamInfo
	seen := make(map[string]struct{}, len(parts))

	for i, p := range parts {
		if !strings.ContainsAny(p, "{}") {
			segs = append(segs, tree.Segment{Kind: tree.KindStatic, Text: p})
			continue
		}
		if len(p) < 3 || p[0] != '{' || p[len(p)-1] != '}' {
			return nil, nil, fmt.Errorf("%w: segment %q must be a whole {name} placeholder", ErrBadPattern, p)
		}
		body := p[1 : len(p)-1]
		if strings.ContainsAny(body, "{}") {
			return nil, nil, fmt.Errorf("%w: nested braces in %q", ErrBadPattern, p)
		}

		catchAll := strings.HasSuffix(body, "...")
		if catchAll {
			body = body[:len(body)-3]
		}
		name, _, hasConstraint := strings.Cut(body, ":")
		if name == "" {
			return nil, nil, fmt.Errorf("%w: empty parameter name in %q", ErrBadPattern, p)
		}
		if hasConstraint {
			return nil, nil, fmt.Errorf(
				"%w: %q constrains %q, which this router does not support; write {%s}",
				ErrBadPattern, p, name, name)
		}
		if _, dup := seen[name]; dup {
			return nil, nil, fmt.Errorf("%w: parameter %q appears twice", ErrBadPattern, name)
		}
		seen[name] = struct{}{}

		seg := tree.Segment{Text: name}
		if catchAll {
			if i != len(parts)-1 {
				return nil, nil, fmt.Errorf("%w: %q is not the last segment", ErrCatchAllPosition, p)
			}
			seg.Kind = tree.KindCatchAll
			// A catch-all edge is distinct from a plain wildcard, so it is given
			// a type the tree can tell apart from an unconstrained parameter.
			seg.Type = "..."
		} else {
			seg.Kind = tree.KindParam
		}
		segs = append(segs, seg)
		params = append(params, ParamInfo{Name: name, Position: i, CatchAll: catchAll})
	}
	return segs, params, nil
}

// contextKey is the private type of the value this package attaches to a
// request.
type contextKey int

const matchKey contextKey = iota

// matchCtx carries the matched route and its parameters. It is the request
// context itself rather than a value inside one, which costs a single
// allocation for the whole match, and it is only attached when the route
// actually declares parameters — a static route writes nothing to the context
// at all.
type matchCtx struct {
	context.Context

	route *Route
	n     int
	arr   [4]PathParam
	spill []PathParam
}

// Value returns the match for this package's key and defers everything else to
// the parent context.
func (c *matchCtx) Value(key any) any {
	if key == matchKey {
		return c
	}
	return c.Context.Value(key)
}

// params returns the captured parameters.
func (c *matchCtx) params() []PathParam {
	if c.spill != nil {
		return c.spill
	}
	return c.arr[:c.n]
}

// withMatch attaches the match to req.
func withMatch(req *http.Request, route *Route, params []PathParam) *http.Request {
	c := &matchCtx{Context: req.Context(), route: route}
	if len(params) <= len(c.arr) {
		c.n = copy(c.arr[:], params)
	} else {
		c.spill = make([]PathParam, len(params))
		copy(c.spill, params)
	}
	return req.WithContext(c)
}

// matchOf returns the match attached to a request, or nil.
func matchOf(r *http.Request) *matchCtx {
	c, _ := r.Context().Value(matchKey).(*matchCtx)
	return c
}

// Param returns the value of the named path parameter, or the empty string if
// the request has no such parameter.
//
// It builds no map and reads no more than the parameters the matched route
// declared.
func Param(r *http.Request, name string) string {
	c := matchOf(r)
	if c == nil {
		return ""
	}
	for _, p := range c.params() {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// ParamAt returns the i'th path parameter in pattern order. It reports false
// when i is out of range.
func ParamAt(r *http.Request, i int) (name, value string, ok bool) {
	c := matchOf(r)
	if c == nil {
		return "", "", false
	}
	ps := c.params()
	if i < 0 || i >= len(ps) {
		return "", "", false
	}
	return ps[i].Name, ps[i].Value, true
}

// NumParams returns how many path parameters the matched route captured.
func NumParams(r *http.Request) int {
	c := matchOf(r)
	if c == nil {
		return 0
	}
	return len(c.params())
}

// AppendParams appends every captured parameter to dst and returns the
// extended slice. It is the allocation-free way to enumerate parameters.
func AppendParams(r *http.Request, dst []PathParam) []PathParam {
	c := matchOf(r)
	if c == nil {
		return dst
	}
	return append(dst, c.params()...)
}

// ParamMap returns the captured parameters as a map. It allocates, and exists
// for callers that need the shape gorilla's Vars returned.
func ParamMap(r *http.Request) map[string]string {
	c := matchOf(r)
	if c == nil {
		return nil
	}
	ps := c.params()
	m := make(map[string]string, len(ps))
	for _, p := range ps {
		m[p.Name] = p.Value
	}
	return m
}

// RouteOf returns the route that matched the request, or nil. A static route
// is reported even though it attaches no parameters.
func RouteOf(r *http.Request) *Route {
	c := matchOf(r)
	if c == nil {
		return nil
	}
	return c.route
}

// SetParams attaches path parameters to a request. It is intended for tests
// that invoke a handler directly.
func SetParams(r *http.Request, params ...PathParam) *http.Request {
	return withMatch(r, RouteOf(r), params)
}
