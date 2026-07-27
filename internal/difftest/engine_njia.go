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

package difftest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"

	njia "github.com/jkaninda/njia/muxcompat"
)

// BuildNjia constructs a muxcompat router from the case.
func BuildNjia(c Case) *njia.Router {
	r := njia.NewRouter()
	if c.Flags.StrictSlash {
		r.StrictSlash(true)
	}
	if c.Flags.SkipClean {
		r.SkipClean(true)
	}
	if c.Flags.UseEncodedPath {
		r.UseEncodedPath()
	}
	if c.Flags.NotFoundHandler {
		r.NotFoundHandler = markerHandler("custom-404", http.StatusNotFound)
	}
	if c.Flags.MethodNotAllowedHandler {
		r.MethodNotAllowedHandler = markerHandler("custom-405", http.StatusMethodNotAllowed)
	}
	for i := 0; i < c.Flags.Middleware; i++ {
		i := i
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Add("X-Mw", strconv.Itoa(i))
				next.ServeHTTP(w, req)
			})
		})
	}
	for i, s := range c.Routes {
		addNjia(r, s, strconv.Itoa(i))
	}
	return r
}

func addNjia(router *njia.Router, s RouteSpec, idx string) {
	name := nameOf(s, idx)
	route := router.NewRoute()
	if s.Host != "" {
		route = route.Host(s.Host)
	}
	if s.PathPrefix != "" {
		route = route.PathPrefix(s.PathPrefix)
	}
	if s.Path != "" {
		route = route.Path(s.Path)
	}
	if s.MethodsEmpty {
		route = route.Methods()
	} else if len(s.Methods) > 0 {
		route = route.Methods(copyOf(s.Methods)...)
	}
	if len(s.Queries) > 0 {
		route = route.Queries(copyOf(s.Queries)...)
	}
	if len(s.Headers) > 0 {
		route = route.Headers(copyOf(s.Headers)...)
	}
	if len(s.HeadersRegexp) > 0 {
		route = route.HeadersRegexp(copyOf(s.HeadersRegexp)...)
	}
	if len(s.Schemes) > 0 {
		route = route.Schemes(copyOf(s.Schemes)...)
	}
	if s.Matcher != "" {
		f := Matchers[s.Matcher]
		route = route.MatcherFunc(func(req *http.Request, _ *njia.RouteMatch) bool { return f(req) })
	}
	if s.BuildOnly {
		route = route.BuildOnly()
	}
	route = route.Name(name)
	if len(s.Sub) > 0 {
		sub := route.Subrouter()
		for j, ss := range s.Sub {
			addNjia(sub, ss, idx+"_"+strconv.Itoa(j))
		}
		return
	}
	if !s.NoHandler {
		route.Handler(njiaHandler(name))
	}
}

func njiaHandler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := ""
		if rt := njia.CurrentRoute(r); rt != nil {
			cur = rt.GetName()
		}
		w.Header().Set("X-Route", cur)
		_, _ = io.WriteString(w, varsBody(name, njia.Vars(r)))
	})
}

// RunNjia executes the case against muxcompat.
func RunNjia(c Case) Result {
	var res Result

	guard(&res, func() {
		r := BuildNjia(c)
		var match njia.RouteMatch
		res.Matched = r.Match(c.request(), &match)
		if match.Route != nil {
			res.MatchedName = match.Route.GetName()
		}
		res.Vars = match.Vars
		if match.MatchErr != nil {
			res.MatchErr = classifyMatchErr(match.MatchErr.Error())
		}

		_ = r.Walk(func(route *njia.Route, _ *njia.Router, _ []*njia.Route) error {
			res.RouteErrs = append(res.RouteErrs, errString(route.GetError()))
			return nil
		})
	})
	if res.Panic != "" {
		return res
	}

	guard(&res, func() {
		r2 := BuildNjia(c)
		rec := httptest.NewRecorder()
		r2.ServeHTTP(rec, c.request())
		res.StatusCode = rec.Code
		res.Location = rec.Header().Get("Location")
		res.Body = rec.Body.String()
		res.Header = headerDigest(rec.Header())
	})
	return res
}

// markerHandler returns a handler that writes a fixed body and status.
func markerHandler(body string, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// copyOf returns a copy of s so that builders which mutate their arguments
// cannot affect the other engine.
func copyOf(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// errString renders an error for comparison. Only the presence and shape of an
// error are compared, not the exact wording, because gorilla's messages are not
// part of its contract.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return classifyErr(err.Error())
}

// classifyErr maps an error message to a stable category so that the two
// engines can be compared without requiring identical wording.
func classifyErr(msg string) string {
	switch {
	case strings.Contains(msg, "must start with a slash"):
		return "path-not-slash"
	case strings.Contains(msg, "multiple of 2"):
		return "odd-pairs"
	case strings.Contains(msg, "duplicated route variable"):
		return "duplicate-var"
	case strings.Contains(msg, "missing name or pattern"),
		strings.Contains(msg, "missing variable name"),
		strings.Contains(msg, "missing variable pattern"):
		return "missing-name-or-pattern"
	case strings.Contains(msg, "unbalanced braces"):
		return "unbalanced-braces"
	case strings.Contains(msg, "already has name"):
		return "already-named"
	case strings.Contains(msg, "doesn't have a host or path"):
		return "no-host-or-path"
	case strings.Contains(msg, "doesn't have a path"), strings.Contains(msg, "does not have a path"):
		return "no-path"
	case strings.Contains(msg, "doesn't have a host"):
		return "no-host"
	case strings.Contains(msg, "doesn't have queries"):
		return "no-queries"
	case strings.Contains(msg, "error parsing regexp"), strings.Contains(msg, "invalid pattern"),
		strings.Contains(msg, "cannot compile"):
		return "bad-regexp"
	default:
		return "other:" + msg
	}
}

// headerDigest renders the response headers that both engines are expected to
// agree on.
func headerDigest(h http.Header) string {
	var parts []string
	for _, k := range []string{"X-Route", "X-Mw", "Access-Control-Allow-Methods"} {
		if vs := h.Values(k); len(vs) > 0 {
			parts = append(parts, k+"="+strings.Join(vs, "|"))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// classifyMatchErr maps a match error to a stable category. CLAUDE.md section
// 1.1 states that the exact sentinel wording need not match gorilla, only the
// behavior callers observe through errors.Is, so the harness compares
// categories rather than message text.
func classifyMatchErr(msg string) string {
	switch {
	case strings.Contains(msg, "not allowed"):
		return "method-mismatch"
	case strings.Contains(msg, "not found"), strings.Contains(msg, "no matching route"):
		return "not-found"
	case msg == "":
		return ""
	default:
		return "other:" + msg
	}
}
