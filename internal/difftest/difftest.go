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

// Package difftest drives gorilla/mux and njia/muxcompat with identical route
// tables and identical requests and reports any divergence in the observable
// outcome.
//
// It is a test-only module: the root njia module has no dependencies, and this
// package lives in its own module so that gorilla can never appear in njia's
// dependency graph.
package difftest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"strings"
)

// Flags are the router-level options a case is run under.
type Flags struct {
	// StrictSlash enables trailing-slash redirection.
	StrictSlash bool
	// SkipClean disables request path cleaning.
	SkipClean bool
	// UseEncodedPath matches routes against the escaped path.
	UseEncodedPath bool
	// NotFoundHandler installs a custom 404 handler.
	NotFoundHandler bool
	// MethodNotAllowedHandler installs a custom 405 handler.
	MethodNotAllowedHandler bool
	// Middleware installs this many router-level middlewares, each of which
	// appends a marker header.
	Middleware int
}

// RouteSpec is an engine-neutral description of one route.
type RouteSpec struct {
	// Name is the route name, used to identify which route matched.
	Name string `json:"name,omitempty"`
	// Path is a full path template.
	Path string `json:"path,omitempty"`
	// PathPrefix is a path prefix template. It is applied before Path.
	PathPrefix string `json:"pathPrefix,omitempty"`
	// Host is a host template.
	Host string `json:"host,omitempty"`
	// Methods restricts the route to these HTTP methods.
	Methods []string `json:"methods,omitempty"`
	// MethodsEmpty applies Methods() with no arguments.
	MethodsEmpty bool `json:"methodsEmpty,omitempty"`
	// Queries is a flat list of query key/value template pairs.
	Queries []string `json:"queries,omitempty"`
	// Headers is a flat list of header key/value pairs.
	Headers []string `json:"headers,omitempty"`
	// HeadersRegexp is a flat list of header key/pattern pairs.
	HeadersRegexp []string `json:"headersRegexp,omitempty"`
	// Schemes restricts the route to these URL schemes.
	Schemes []string `json:"schemes,omitempty"`
	// BuildOnly marks the route as non-matching.
	BuildOnly bool `json:"buildOnly,omitempty"`
	// Matcher names a custom matcher from the registry.
	Matcher string `json:"matcher,omitempty"`
	// Sub declares routes on a subrouter of this route.
	Sub []RouteSpec `json:"sub,omitempty"`
	// NoHandler leaves the route without a handler.
	NoHandler bool `json:"noHandler,omitempty"`
}

// Matchers is the registry of custom matchers a RouteSpec may refer to by
// name. Both engines are given the same predicate.
var Matchers = map[string]func(*http.Request) bool{
	"always":     func(*http.Request) bool { return true },
	"never":      func(*http.Request) bool { return false },
	"hasQueryX":  func(r *http.Request) bool { return r.URL.Query().Has("x") },
	"pathIsEven": func(r *http.Request) bool { return len(r.URL.Path)%2 == 0 },
}

// Case is one differential scenario: a route table, router flags and a
// request.
type Case struct {
	// Name identifies the case in test output.
	Name string `json:"name,omitempty"`
	// Routes is the route table, in registration order.
	Routes []RouteSpec `json:"routes"`
	// Flags are the router options.
	Flags Flags `json:"flags,omitempty"`
	// Method is the request method.
	Method string `json:"method,omitempty"`
	// Target is the request URL.
	Target string `json:"target"`
	// Header carries request headers.
	Header map[string][]string `json:"header,omitempty"`
	// Host overrides the request Host header.
	Host string `json:"host,omitempty"`
	// TLS marks the request as arriving over TLS.
	TLS bool `json:"tls,omitempty"`
}

// Result is the observable outcome of running a case against one engine.
type Result struct {
	// MatchedName is the name of the route that matched, if any.
	MatchedName string
	// Matched reports whether the router's Match method returned true.
	Matched bool
	// Vars are the variables captured by the match.
	Vars map[string]string
	// MatchErr is the string form of RouteMatch.MatchErr.
	MatchErr string
	// StatusCode is the status written by ServeHTTP.
	StatusCode int
	// Location is the Location response header, if any.
	Location string
	// Body is the response body written by ServeHTTP.
	Body string
	// RouteErrs lists the build errors of the registered routes, in order.
	RouteErrs []string
	// Header carries selected response headers set by middleware.
	Header string
	// Panic records the value of a panic raised while building the route table
	// or serving the request. gorilla panics in a few places and muxcompat has
	// to panic in the same ones.
	Panic string
}

// String renders the result for diff output.
func (r Result) String() string {
	keys := make([]string, 0, len(r.Vars))
	for k := range r.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "matched=%v name=%q vars={", r.Matched, r.MatchedName)
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%s", k, r.Vars[k])
	}
	fmt.Fprintf(&b, "} matchErr=%q status=%d location=%q body=%q hdr=%q routeErrs=%v panic=%q",
		r.MatchErr, r.StatusCode, r.Location, r.Body, r.Header, r.RouteErrs, r.Panic)
	return b.String()
}

// guard runs fn and records a panic into res instead of letting it escape, so
// that the two engines can be compared on panic behavior as well.
func guard(res *Result, fn func()) {
	defer func() {
		if v := recover(); v != nil {
			res.Panic = classifyPanic(v)
		}
	}()
	fn()
}

// classifyPanic reduces a panic value to a stable category.
//
// Runtime faults are prefixed "runtime:" and are treated differently from
// deliberate panics: a deliberate panic is part of the contract muxcompat has
// to reproduce, while a runtime fault is a bug in whichever engine raised it.
func classifyPanic(v any) string {
	msg := fmt.Sprint(v)
	if _, ok := v.(runtime.Error); ok {
		switch {
		case strings.Contains(msg, "nil pointer dereference"):
			return "runtime:nil-deref"
		case strings.Contains(msg, "index out of range"):
			return "runtime:index-range"
		case strings.Contains(msg, "slice bounds out of range"):
			return "runtime:slice-range"
		default:
			return "runtime:" + msg
		}
	}
	return "deliberate:" + msg
}

// IsCrash reports whether a classified panic is a runtime fault rather than a
// deliberate, contractual panic.
func IsCrash(p string) bool { return strings.HasPrefix(p, "runtime:") }

// Equal reports whether two results are observationally identical.
func (r Result) Equal(o Result) bool { return r.String() == o.String() }

// request builds the case's request. A fresh request is built for each run so
// that context values set by one engine cannot leak into the other.
func (c Case) request() *http.Request {
	method := c.Method
	if method == "" {
		method = http.MethodGet
	}
	req := httptest.NewRequest(method, c.Target, nil)
	for k, vs := range c.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if c.Host != "" {
		req.Host = c.Host
	}
	if !c.TLS {
		req.TLS = nil
	}
	return req
}

// varsBody renders a handler response that encodes the matched route and its
// variables, so that a divergence in either is visible in the body.
func varsBody(name string, vars map[string]string) string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("route=")
	b.WriteString(name)
	b.WriteString(";vars=")
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(vars[k])
	}
	return b.String()
}

// nameOf returns a stable identifier for an unnamed route.
func nameOf(s RouteSpec, idx string) string {
	if s.Name != "" {
		return s.Name
	}
	return "r" + idx
}
