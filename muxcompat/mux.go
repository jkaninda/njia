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
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sync"

	"github.com/jkaninda/njia/internal/tree"
)

// Sentinel errors reported through RouteMatch.MatchErr and Walk.
var (
	// ErrNotFound is returned when no route matched the request.
	ErrNotFound = errors.New("mux: route not found")
	// ErrMethodMismatch is returned when a route matched the request path but
	// not its method.
	ErrMethodMismatch = errors.New("mux: method is not allowed")
	// SkipRouter is returned by a WalkFunc to skip the subtree rooted at the
	// route currently being visited.
	SkipRouter = errors.New("mux: skip traversing this router branch")
)

// MatcherFunc is an adapter that allows an ordinary function to be used as a
// custom route matcher.
type MatcherFunc func(*http.Request, *RouteMatch) bool

// Match calls f(req, match).
func (f MatcherFunc) Match(req *http.Request, match *RouteMatch) bool {
	return f(req, match)
}

// MiddlewareFunc wraps an http.Handler, returning the handler that should be
// invoked in its place.
type MiddlewareFunc func(http.Handler) http.Handler

// Middleware applies the function to handler.
func (f MiddlewareFunc) Middleware(handler http.Handler) http.Handler {
	return f(handler)
}

// BuildVarsFunc transforms the variables supplied to reverse URL building
// before the template is rendered.
type BuildVarsFunc func(map[string]string) map[string]string

// WalkFunc is called by Router.Walk for every route in a router tree.
// Returning SkipRouter skips the branch below the current route.
type WalkFunc func(route *Route, router *Router, ancestors []*Route) error

// matcher is anything that can decide whether a request satisfies part of a
// route.
type matcher interface {
	Match(*http.Request, *RouteMatch) bool
}

// RouteMatch describes the outcome of matching a request against a router.
type RouteMatch struct {
	// Route is the route that matched, if any.
	Route *Route
	// Handler is the handler that should serve the request.
	Handler http.Handler
	// Vars holds the variables captured from the host, path and query
	// templates of the matched route.
	Vars map[string]string
	// MatchErr is ErrNotFound or ErrMethodMismatch when matching failed, and
	// nil otherwise.
	MatchErr error
}

// setVar records a captured variable, allocating the map on first use.
func (m *RouteMatch) setVar(name, value string) {
	if m.Vars == nil {
		m.Vars = make(map[string]string)
	}
	m.Vars[name] = value
}

// routeConf is the configuration a router hands down to the routes it creates
// and that a route hands down to its subrouter.
type routeConf struct {
	useEncodedPath bool
	strictSlash    bool
	skipClean      bool
	regexp         routeRegexpGroup
	matchers       []matcher
	buildScheme    string
	buildVarsFunc  BuildVarsFunc

	// seqCounter is shared by every router and route in a tree so that
	// registration sequence numbers are globally ordered.
	seqCounter *int
}

// copyRouteConf returns a deep enough copy of c that mutating the copy's
// slices cannot affect the original.
func copyRouteConf(c routeConf) routeConf {
	out := c
	out.regexp = copyGroup(c.regexp)
	if len(c.matchers) > 0 {
		out.matchers = make([]matcher, len(c.matchers))
		copy(out.matchers, c.matchers)
	}
	return out
}

// Router registers routes and dispatches requests to the first one that
// matches.
type Router struct {
	// NotFoundHandler serves requests that matched no route. When nil,
	// http.NotFoundHandler is used.
	NotFoundHandler http.Handler

	// MethodNotAllowedHandler serves requests whose path matched a route but
	// whose method did not. When nil, a handler writing 405 is used.
	MethodNotAllowedHandler http.Handler

	routeConf

	routes      []*Route
	namedRoutes map[string]*Route
	middlewares []MiddlewareFunc

	// index is the lazily built lookup accelerator described in the package
	// documentation. It is invalidated whenever a route is added.
	index atomicIndex

	// noIndex forces the plain ordered scan. It exists so that tests can run
	// the same table both ways and require identical results.
	noIndex bool
}

// NewRouter returns an empty router.
func NewRouter() *Router {
	seq := 0
	return &Router{
		namedRoutes: make(map[string]*Route),
		routeConf:   routeConf{seqCounter: &seq},
	}
}

// matchState carries the parameters captured on the indexed path. Keeping them
// in a caller-owned buffer lets a matched request avoid building the variable
// map until something actually asks for it.
type matchState struct {
	lazy   bool
	params []tree.Param
	buf    [4]tree.Param
}

// Match attempts to match the request against the router's routes, filling in
// match. It reports whether a handler was selected.
func (r *Router) Match(req *http.Request, match *RouteMatch) bool {
	return r.match(req, match, nil)
}

func (r *Router) match(req *http.Request, match *RouteMatch, st *matchState) bool {
	if r.matchRoutes(req, match, st) {
		if match.MatchErr == nil {
			for i := len(r.middlewares) - 1; i >= 0; i-- {
				match.Handler = r.middlewares[i].Middleware(match.Handler)
			}
		}
		return true
	}

	if errors.Is(match.MatchErr, ErrMethodMismatch) {
		if r.MethodNotAllowedHandler != nil {
			match.Handler = r.MethodNotAllowedHandler
			return true
		}
		return false
	}

	if r.NotFoundHandler != nil {
		match.Handler = r.NotFoundHandler
		match.MatchErr = ErrNotFound
		return true
	}

	match.MatchErr = ErrNotFound
	return false
}

// matchRoutes evaluates the route table. It uses the lookup index when one is
// available and falls back to the ordered scan otherwise; both produce
// identical results.
func (r *Router) matchRoutes(req *http.Request, match *RouteMatch, st *matchState) bool {
	idx := r.currentIndex()
	if idx == nil {
		return r.scanFrom(0, req, match)
	}

	path := requestPath(req, idx.useEncodedPath)
	var idBuf [24]int32
	cands := idx.candidates(path, idBuf[:0])

	for _, i := range cands {
		route := r.routes[i]
		e := &idx.entries[i]
		if e.fast {
			if route.matchFast(e, req, match, path, st) {
				return true
			}
		} else if route.Match(req, match) {
			return true
		}
		if errors.Is(match.MatchErr, ErrNotFound) {
			// Only a nested router or a custom matcher can leave ErrNotFound
			// behind mid-scan. A route the index skipped would have cleared
			// it, so from here on the ordered scan is the only safe way to
			// stay identical to gorilla.
			return r.scanFrom(int(i)+1, req, match)
		}
	}
	return false
}

// scanFrom evaluates every route from start onwards in registration order.
func (r *Router) scanFrom(start int, req *http.Request, match *RouteMatch) bool {
	for _, route := range r.routes[start:] {
		if route.Match(req, match) {
			return true
		}
	}
	return false
}

// currentIndex returns the router's lookup index, rebuilding it when the route
// table or the path form has changed. It returns nil when the table is too
// small or too irregular for indexing to pay off.
func (r *Router) currentIndex() *routeIndex {
	if r.noIndex {
		return nil
	}
	idx := r.index.load()
	if idx != nil && idx.routeCount == len(r.routes) && idx.useEncodedPath == r.useEncodedPath {
		return idx
	}
	idx = r.buildIndex()
	if idx == nil {
		r.index.invalidate()
		return nil
	}
	r.index.store(idx)
	return idx
}

// ServeHTTP dispatches the request to the handler of the first matching route.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !r.skipClean {
		p := requestPath(req, r.useEncodedPath)
		if cp := cleanPath(p); cp != p {
			u := *req.URL
			u.Path = cp
			target := u.String()
			w.Header().Set("Location", target)
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
	}

	var match RouteMatch
	var handler http.Handler
	st := matchState{lazy: true}
	st.params = st.buf[:0]
	if r.match(req, &match, &st) {
		handler = match.Handler
		req = requestWithMatch(req, match.Route, match.Vars, st.params)
	}

	if handler == nil && errors.Is(match.MatchErr, ErrMethodMismatch) {
		handler = methodNotAllowedHandler()
	}
	if handler == nil {
		handler = http.NotFoundHandler()
	}
	handler.ServeHTTP(w, req)
}

// Get returns the route registered with the given name.
func (r *Router) Get(name string) *Route { return r.namedRoutes[name] }

// GetRoute returns the route registered with the given name.
func (r *Router) GetRoute(name string) *Route { return r.namedRoutes[name] }

// StrictSlash configures whether a request whose path differs from a matched
// route's template only by a trailing slash is redirected to the template's
// form. It returns the router so calls can be chained.
func (r *Router) StrictSlash(value bool) *Router {
	r.strictSlash = value
	return r
}

// SkipClean configures whether the router leaves the request path untouched
// instead of redirecting to its cleaned form.
func (r *Router) SkipClean(value bool) *Router {
	r.skipClean = value
	return r
}

// UseEncodedPath makes routes match against the raw, still-escaped path
// instead of the decoded one.
func (r *Router) UseEncodedPath() *Router {
	r.useEncodedPath = true
	return r
}

// NewRoute registers an empty route on the router.
func (r *Router) NewRoute() *Route {
	if r.namedRoutes == nil {
		r.namedRoutes = make(map[string]*Route)
	}
	route := &Route{
		routeConf:   copyRouteConf(r.routeConf),
		namedRoutes: r.namedRoutes,
		seq:         r.nextSeq(),
	}
	r.routes = append(r.routes, route)
	r.index.invalidate()
	return route
}

// nextSeq returns the next global registration sequence number.
func (r *Router) nextSeq() int {
	if r.seqCounter == nil {
		seq := 0
		r.seqCounter = &seq
	}
	*r.seqCounter++
	return *r.seqCounter
}

// Name registers a new route with the given name.
func (r *Router) Name(name string) *Route { return r.NewRoute().Name(name) }

// Handle registers a new route with the given path and handler.
func (r *Router) Handle(path string, handler http.Handler) *Route {
	return r.NewRoute().Path(path).Handler(handler)
}

// HandleFunc registers a new route with the given path and handler function.
func (r *Router) HandleFunc(path string, f func(http.ResponseWriter, *http.Request)) *Route {
	return r.NewRoute().Path(path).HandlerFunc(f)
}

// Headers registers a new route with the given header key/value pairs.
func (r *Router) Headers(pairs ...string) *Route { return r.NewRoute().Headers(pairs...) }

// Host registers a new route with the given host template.
func (r *Router) Host(tpl string) *Route { return r.NewRoute().Host(tpl) }

// MatcherFunc registers a new route with the given custom matcher.
func (r *Router) MatcherFunc(f MatcherFunc) *Route { return r.NewRoute().MatcherFunc(f) }

// Methods registers a new route with the given HTTP methods.
func (r *Router) Methods(methods ...string) *Route { return r.NewRoute().Methods(methods...) }

// Path registers a new route with the given path template.
func (r *Router) Path(tpl string) *Route { return r.NewRoute().Path(tpl) }

// PathPrefix registers a new route with the given path prefix template.
func (r *Router) PathPrefix(tpl string) *Route { return r.NewRoute().PathPrefix(tpl) }

// Queries registers a new route with the given query key/value pairs.
func (r *Router) Queries(pairs ...string) *Route { return r.NewRoute().Queries(pairs...) }

// Schemes registers a new route with the given URL schemes.
func (r *Router) Schemes(schemes ...string) *Route { return r.NewRoute().Schemes(schemes...) }

// BuildVarsFunc registers a new route with the given reverse-build hook.
func (r *Router) BuildVarsFunc(f BuildVarsFunc) *Route { return r.NewRoute().BuildVarsFunc(f) }

// Use appends middleware to the router's chain. Middleware runs outermost
// first, in registration order, and only wraps handlers of routes that
// matched.
func (r *Router) Use(mwf ...MiddlewareFunc) {
	r.middlewares = append(r.middlewares, mwf...)
}

// Walk visits every route in the router tree in registration order, depth
// first, calling walkFn for each.
func (r *Router) Walk(walkFn WalkFunc) error {
	return r.walk(walkFn, []*Route{})
}

func (r *Router) walk(walkFn WalkFunc, ancestors []*Route) error {
	for _, t := range r.routes {
		err := walkFn(t, r, ancestors)
		if errors.Is(err, SkipRouter) {
			continue
		}
		if err != nil {
			return err
		}
		for _, m := range t.matchers {
			sr, ok := m.(*Router)
			if !ok {
				continue
			}
			ancestors = append(ancestors, t)
			if err := sr.walk(walkFn, ancestors); err != nil {
				return err
			}
			ancestors = ancestors[:len(ancestors)-1]
		}
		if sr, ok := t.handler.(*Router); ok {
			ancestors = append(ancestors, t)
			if err := sr.walk(walkFn, ancestors); err != nil {
				return err
			}
			ancestors = ancestors[:len(ancestors)-1]
		}
	}
	return nil
}

// contextKey is the private type used for the value this package stores in a
// request context.
type contextKey int

const matchKey contextKey = iota

// matchCtx carries the matched route and its variables through the request.
//
// It is itself the request context rather than a value inside one, which saves
// an allocation per matched request, and the variable map is materialized on
// first use so that handlers which never call Vars do not pay for it at all.
type matchCtx struct {
	context.Context

	route *Route

	once sync.Once
	vars map[string]string

	// n and arr hold parameters captured on the indexed path. They are copied
	// out of the matcher's scratch buffer, so nothing the request carries
	// points back into a reused buffer.
	n     int
	arr   [4]tree.Param
	spill []tree.Param
}

// Value returns the match itself for this package's key and defers everything
// else to the parent context.
func (c *matchCtx) Value(key any) any {
	if key == matchKey {
		return c
	}
	return c.Context.Value(key)
}

// varsMap returns the variables as a map, building it on first call.
func (c *matchCtx) varsMap() map[string]string {
	c.once.Do(func() {
		if c.vars != nil {
			return
		}
		c.vars = make(map[string]string, c.n+len(c.spill))
		for i := 0; i < c.n; i++ {
			c.vars[c.arr[i].Name] = c.arr[i].Value
		}
		for _, p := range c.spill {
			c.vars[p.Name] = p.Value
		}
	})
	return c.vars
}

// requestWithMatch returns a shallow copy of req carrying the match result.
// Either vars is already built, for routes matched by the ordered scan, or
// params holds what the indexed path captured.
func requestWithMatch(req *http.Request, route *Route, vars map[string]string, params []tree.Param) *http.Request {
	c := &matchCtx{Context: req.Context(), route: route, vars: vars}
	if vars == nil {
		if len(params) <= len(c.arr) {
			c.n = copy(c.arr[:], params)
		} else {
			c.spill = make([]tree.Param, len(params))
			copy(c.spill, params)
		}
	}
	return req.WithContext(c)
}

// Vars returns the route variables captured for the current request, or nil if
// no route matched.
func Vars(r *http.Request) map[string]string {
	c, _ := r.Context().Value(matchKey).(*matchCtx)
	if c == nil {
		return nil
	}
	return c.varsMap()
}

// CurrentRoute returns the route that matched the current request, or nil.
func CurrentRoute(r *http.Request) *Route {
	c, _ := r.Context().Value(matchKey).(*matchCtx)
	if c == nil {
		return nil
	}
	return c.route
}

// SetURLVars sets the route variables for the request. It is intended for
// tests that invoke a handler directly.
func SetURLVars(r *http.Request, val map[string]string) *http.Request {
	c := &matchCtx{Context: r.Context(), vars: val}
	c.once.Do(func() {})
	if existing, ok := r.Context().Value(matchKey).(*matchCtx); ok {
		c.route = existing.route
	}
	return r.WithContext(c)
}

// cleanPath returns the canonical form of p, preserving a trailing slash.
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	if p[0] != '/' {
		p = "/" + p
	}
	np := path.Clean(p)
	if p[len(p)-1] == '/' && np != "/" {
		np += "/"
	}
	return np
}

// methodNotAllowedHandler returns the default 405 handler.
func methodNotAllowedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
}

// uniqueVars reports an error when the two name sets overlap.
func uniqueVars(a, b []string) error {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return fmt.Errorf("mux: duplicated route variable %q", x)
			}
		}
	}
	return nil
}
