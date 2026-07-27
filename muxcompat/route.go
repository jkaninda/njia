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

package muxcompat

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jkaninda/njia/internal/tree"
)

// Route is a request handler together with the conditions a request must
// satisfy to reach it.
type Route struct {
	routeConf

	handler     http.Handler
	buildOnly   bool
	name        string
	err         error
	namedRoutes map[string]*Route

	// seq is the route's global registration order. It is used by the lookup
	// index to preserve gorilla's first-registered-wins semantics across data
	// structures.
	seq int
}

// SkipClean reports whether the route's router leaves request paths uncleaned.
func (r *Route) SkipClean() bool { return r.skipClean }

// Match reports whether the request satisfies every one of the route's
// matchers, filling in match on success.
func (r *Route) Match(req *http.Request, match *RouteMatch) bool {
	if r.buildOnly || r.err != nil {
		return false
	}

	var matchErr error
	progressed := false
	for _, m := range r.matchers {
		if m.Match(req, match) {
			progressed = true
			continue
		}
		if _, ok := m.(methodMatcher); ok {
			matchErr = ErrMethodMismatch
			continue
		}
		if _, nested := m.(*Router); nested {
			// A nested router is the one kind of matcher that reports its own
			// diagnosis, so its verdict is kept: a method mismatch found
			// inside the subrouter still produces a 405. The exception is
			// ErrNotFound, which must not leak out, or a later sibling route
			// would be treated as an error case and skip middleware.
			if errors.Is(match.MatchErr, ErrNotFound) {
				match.MatchErr = nil
			}
			return false
		}
		if progressed || errors.Is(match.MatchErr, ErrNotFound) {
			// This route got further than whichever earlier route recorded the
			// pending error, so that error no longer describes the request: a
			// 405 left behind by a sibling must not survive a route that
			// matched the path and then failed on a query, header, scheme or
			// custom matcher.
			match.MatchErr = nil
		}
		return false
	}

	if matchErr != nil {
		match.MatchErr = matchErr
		return false
	}

	if errors.Is(match.MatchErr, ErrMethodMismatch) && r.handler != nil {
		// An earlier route matched the path but not the method; this one
		// matches both, so it wins and the earlier error is cleared.
		match.MatchErr = nil
		match.Handler = r.handler
	}

	if match.Route == nil {
		match.Route = r
	}
	if match.Handler == nil {
		match.Handler = r.handler
	}
	if match.Vars == nil {
		match.Vars = make(map[string]string)
	}

	r.regexp.setMatch(req, match, r)
	return true
}

// matchFast completes a match for a route the lookup index already proved the
// path of. It reproduces exactly what Match would do for a route whose only
// matchers are that path template and an optional method filter, without
// running a regular expression.
func (r *Route) matchFast(e *indexEntry, req *http.Request, match *RouteMatch, path string, st *matchState) bool {
	if e.hasMethods && !matchInArray(e.methods, req.Method) {
		match.MatchErr = ErrMethodMismatch
		return false
	}

	if errors.Is(match.MatchErr, ErrMethodMismatch) && r.handler != nil {
		match.MatchErr = nil
		match.Handler = r.handler
	}
	if match.Route == nil {
		match.Route = r
	}
	if match.Handler == nil {
		match.Handler = r.handler
	}

	if st != nil && st.lazy {
		if e.nparams > 0 {
			st.params, _ = tree.Extract(e.segs, path, st.params[:0])
		}
		return true
	}

	if match.Vars == nil {
		match.Vars = make(map[string]string, e.nparams)
	}
	if e.nparams > 0 {
		var buf [8]tree.Param
		params, _ := tree.Extract(e.segs, path, buf[:0])
		for _, p := range params {
			match.Vars[p.Name] = p.Value
		}
	}
	return true
}

// GetError returns the first error accumulated while building the route.
func (r *Route) GetError() error { return r.err }

// addErr records err as the route's error if none was recorded yet.
func (r *Route) addErr(err error) *Route {
	if r.err == nil {
		r.err = err
	}
	return r
}

// addMatcher appends a matcher to the route.
func (r *Route) addMatcher(m matcher) *Route {
	if r.err == nil {
		r.matchers = append(r.matchers, m)
	}
	return r
}

// addRegexpMatcher compiles tpl and installs it as a host, path, prefix or
// query matcher.
func (r *Route) addRegexpMatcher(tpl string, kind regexpKind) error {
	if r.err != nil {
		return r.err
	}
	if kind == regexpKindPath || kind == regexpKindPrefix {
		// An empty template is legal: on a subrouter it means "the parent's
		// prefix, unchanged".
		if len(tpl) > 0 && tpl[0] != '/' {
			return fmt.Errorf("mux: path must start with a slash, got %q", tpl)
		}
		if r.regexp.path != nil {
			tpl = strings.TrimRight(r.regexp.path.tpl.Raw, "/") + tpl
		}
	}
	rr, err := newRouteRegexp(tpl, kind, routeOptions{
		strictSlash:    r.strictSlash,
		useEncodedPath: r.useEncodedPath,
	})
	if err != nil {
		return err
	}
	for _, q := range r.regexp.queries {
		if err := uniqueVars(rr.varNames(), q.varNames()); err != nil {
			return err
		}
	}
	if kind == regexpKindHost {
		if r.regexp.path != nil {
			if err := uniqueVars(rr.varNames(), r.regexp.path.varNames()); err != nil {
				return err
			}
		}
		r.regexp.host = rr
	} else {
		if r.regexp.host != nil {
			if err := uniqueVars(rr.varNames(), r.regexp.host.varNames()); err != nil {
				return err
			}
		}
		if kind == regexpKindQuery {
			r.regexp.queries = append(r.regexp.queries, rr)
		} else {
			r.regexp.path = rr
		}
	}
	r.addMatcher(rr)
	return nil
}

// Handler sets the handler for the route.
func (r *Route) Handler(handler http.Handler) *Route {
	if r.err == nil {
		r.handler = handler
	}
	return r
}

// HandlerFunc sets the handler function for the route.
func (r *Route) HandlerFunc(f func(http.ResponseWriter, *http.Request)) *Route {
	return r.Handler(http.HandlerFunc(f))
}

// GetHandler returns the route's handler, if any.
func (r *Route) GetHandler() http.Handler { return r.handler }

// Name sets the name of the route, which must not be set already and must be
// unique within the router tree.
func (r *Route) Name(name string) *Route {
	if r.name != "" {
		return r.addErr(fmt.Errorf("mux: route already has name %q, can't set %q", r.name, name))
	}
	if r.err == nil {
		r.name = name
		r.namedRoutes[name] = r
	}
	return r
}

// GetName returns the name of the route, or the empty string if unnamed.
func (r *Route) GetName() string { return r.name }

// Headers adds a matcher requiring the given header key/value pairs. A pair
// whose value is empty requires only that the header be present.
func (r *Route) Headers(pairs ...string) *Route {
	if r.err != nil {
		return r
	}
	headers, err := mapFromPairsToString(pairs...)
	if err != nil {
		return r.addErr(fmt.Errorf("mux: number of parameters must be multiple of 2, got %v", pairs))
	}
	return r.addMatcher(headerMatcher(headers))
}

// HeadersRegexp adds a matcher requiring the given header keys to be present
// with values matching the given patterns.
func (r *Route) HeadersRegexp(pairs ...string) *Route {
	if r.err != nil {
		return r
	}
	headers, err := mapFromPairsToRegex(pairs...)
	if err != nil {
		return r.addErr(fmt.Errorf("mux: number of parameters must be multiple of 2, got %v", pairs))
	}
	return r.addMatcher(headerRegexpMatcher(headers))
}

// Host adds a matcher for the URL host, given as a template.
func (r *Route) Host(tpl string) *Route {
	if err := r.addRegexpMatcher(tpl, regexpKindHost); err != nil {
		r.addErr(err)
	}
	return r
}

// MatcherFunc adds a custom matcher to the route.
func (r *Route) MatcherFunc(f MatcherFunc) *Route { return r.addMatcher(f) }

// Methods adds a matcher for HTTP methods. Method names are upper-cased.
func (r *Route) Methods(methods ...string) *Route {
	for i, m := range methods {
		methods[i] = strings.ToUpper(m)
	}
	return r.addMatcher(methodMatcher(methods))
}

// Path adds a matcher for the URL path, given as a template. When the route
// belongs to a subrouter the parent's path template is prepended.
func (r *Route) Path(tpl string) *Route {
	if err := r.addRegexpMatcher(tpl, regexpKindPath); err != nil {
		r.addErr(err)
	}
	return r
}

// PathPrefix adds a matcher for a leading portion of the URL path.
func (r *Route) PathPrefix(tpl string) *Route {
	if err := r.addRegexpMatcher(tpl, regexpKindPrefix); err != nil {
		r.addErr(err)
	}
	return r
}

// Queries adds a matcher for URL query values, given as key/value pairs. A
// pair whose value is empty matches the key with any value.
//
// For compatibility with gorilla, an odd number of arguments records an error
// on the route and returns nil rather than the route, so chaining another
// builder onto the result panics. This is surprising but it is gorilla's
// observable behavior and callers may depend on it.
func (r *Route) Queries(pairs ...string) *Route {
	if len(pairs)%2 != 0 {
		r.addErr(fmt.Errorf("mux: number of parameters must be multiple of 2, got %v", pairs))
		return nil
	}
	for i := 0; i < len(pairs); i += 2 {
		if err := r.addRegexpMatcher(pairs[i]+"="+pairs[i+1], regexpKindQuery); err != nil {
			return r.addErr(err)
		}
	}
	return r
}

// Schemes adds a matcher for URL schemes. Scheme names are lower-cased. The
// first scheme is used when building URLs in reverse.
func (r *Route) Schemes(schemes ...string) *Route {
	for i, s := range schemes {
		schemes[i] = strings.ToLower(s)
	}
	if len(schemes) > 0 {
		r.buildScheme = schemes[0]
	}
	return r.addMatcher(schemeMatcher(schemes))
}

// BuildVarsFunc sets a function to transform variables before a URL is built.
func (r *Route) BuildVarsFunc(f BuildVarsFunc) *Route {
	if r.buildVarsFunc != nil {
		old := r.buildVarsFunc
		r.buildVarsFunc = func(m map[string]string) map[string]string {
			return f(old(m))
		}
	} else {
		r.buildVarsFunc = f
	}
	return r
}

// BuildOnly marks the route as not matching any request. It can still be used
// for reverse URL building.
func (r *Route) BuildOnly() *Route {
	r.buildOnly = true
	return r
}

// Subrouter returns a router nested under the route, inheriting its matchers
// and configuration.
func (r *Route) Subrouter() *Router {
	sr := &Router{
		routeConf:   copyRouteConf(r.routeConf),
		namedRoutes: r.namedRoutes,
	}
	r.addMatcher(sr)
	return sr
}

// URL builds a URL for the route from the given key/value pairs.
func (r *Route) URL(pairs ...string) (*url.URL, error) {
	if r.err != nil {
		return nil, r.err
	}
	values, err := r.prepareVars(pairs...)
	if err != nil {
		return nil, err
	}
	var scheme, host, path string
	queries := make([]string, 0, len(r.regexp.queries))
	if r.regexp.host != nil {
		if host, err = r.regexp.host.url(values); err != nil {
			return nil, err
		}
		scheme = "http"
		if r.buildScheme != "" {
			scheme = r.buildScheme
		}
	}
	if r.regexp.path != nil {
		if path, err = r.regexp.path.url(values); err != nil {
			return nil, err
		}
	}
	if r.regexp.host == nil && r.regexp.path == nil {
		return nil, errors.New("mux: route doesn't have a host or path")
	}
	for _, q := range r.regexp.queries {
		s, err := q.url(values)
		if err != nil {
			return nil, err
		}
		queries = append(queries, s)
	}
	return &url.URL{
		Scheme:   scheme,
		Host:     host,
		Path:     path,
		RawQuery: strings.Join(queries, "&"),
	}, nil
}

// URLHost builds only the host part of the route's URL.
func (r *Route) URLHost(pairs ...string) (*url.URL, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.regexp.host == nil {
		return nil, errors.New("mux: route doesn't have a host")
	}
	values, err := r.prepareVars(pairs...)
	if err != nil {
		return nil, err
	}
	host, err := r.regexp.host.url(values)
	if err != nil {
		return nil, err
	}
	u := &url.URL{Scheme: "http", Host: host}
	if r.buildScheme != "" {
		u.Scheme = r.buildScheme
	}
	return u, nil
}

// URLPath builds only the path part of the route's URL.
func (r *Route) URLPath(pairs ...string) (*url.URL, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.regexp.path == nil {
		return nil, errors.New("mux: route doesn't have a path")
	}
	values, err := r.prepareVars(pairs...)
	if err != nil {
		return nil, err
	}
	path, err := r.regexp.path.url(values)
	if err != nil {
		return nil, err
	}
	return &url.URL{Path: path}, nil
}

// GetPathTemplate returns the path template of the route, if it has one.
func (r *Route) GetPathTemplate() (string, error) {
	if r.err != nil {
		return "", r.err
	}
	if r.regexp.path == nil {
		return "", errors.New("mux: route doesn't have a path")
	}
	return r.regexp.path.tpl.Raw, nil
}

// GetPathRegexp returns the source of the compiled path regular expression.
func (r *Route) GetPathRegexp() (string, error) {
	if r.err != nil {
		return "", r.err
	}
	if r.regexp.path == nil {
		return "", errors.New("mux: route does not have a path")
	}
	return r.regexp.path.tpl.Source, nil
}

// GetQueriesRegexp returns the sources of the compiled query regular
// expressions.
func (r *Route) GetQueriesRegexp() ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.regexp.queries == nil {
		return nil, errors.New("mux: route doesn't have queries")
	}
	out := make([]string, 0, len(r.regexp.queries))
	for _, q := range r.regexp.queries {
		out = append(out, q.tpl.Source)
	}
	return out, nil
}

// GetQueriesTemplates returns the query templates of the route.
func (r *Route) GetQueriesTemplates() ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.regexp.queries == nil {
		return nil, errors.New("mux: route doesn't have queries")
	}
	out := make([]string, 0, len(r.regexp.queries))
	for _, q := range r.regexp.queries {
		out = append(out, q.tpl.Raw)
	}
	return out, nil
}

// GetMethods returns the HTTP methods the route matches, if it constrains
// them.
func (r *Route) GetMethods() ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	for _, m := range r.matchers {
		if methods, ok := m.(methodMatcher); ok {
			return []string(methods), nil
		}
	}
	return nil, nil
}

// GetHostTemplate returns the host template of the route, if it has one.
func (r *Route) GetHostTemplate() (string, error) {
	if r.err != nil {
		return "", r.err
	}
	if r.regexp.host == nil {
		return "", errors.New("mux: route doesn't have a host")
	}
	return r.regexp.host.tpl.Raw, nil
}

// GetVarNames returns the names of all variables the route declares, across
// its host, path and query templates.
func (r *Route) GetVarNames() ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	var names []string
	if r.regexp.host != nil {
		names = append(names, r.regexp.host.varNames()...)
	}
	if r.regexp.path != nil {
		names = append(names, r.regexp.path.varNames()...)
	}
	for _, q := range r.regexp.queries {
		names = append(names, q.varNames()...)
	}
	return names, nil
}

// prepareVars converts key/value pairs to a map and applies the route's
// BuildVarsFunc chain.
func (r *Route) prepareVars(pairs ...string) (map[string]string, error) {
	m, err := mapFromPairsToString(pairs...)
	if err != nil {
		return nil, err
	}
	return r.buildVars(m), nil
}

func (r *Route) buildVars(m map[string]string) map[string]string {
	if r.buildVarsFunc != nil {
		m = r.buildVarsFunc(m)
	}
	return m
}
