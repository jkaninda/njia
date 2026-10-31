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

/*
Package njia is a zero-dependency HTTP router for Go.

It routes with a segment-indexed prefix tree, captures path parameters without
building a map, and reports its own route table so that documentation can be
generated from the routes themselves rather than reconstructed from compiled
regular expressions.

	r := njia.New()
	r.GET("/users/{id}", http.HandlerFunc(getUser))

	api := r.Group("/api/v1", authMiddleware)
	api.GET("/orders/{id}", http.HandlerFunc(getOrder))

Registration returns an error instead of panicking, and Swap replaces the whole
route table atomically after validating it, so a process that builds routes from
user-supplied configuration can reload without ever going down.

# Proxies and gateways

Two features exist for reverse proxies, which route on behalf of backends
rather than serving their own handlers.

ANY registers one handler for every HTTP method, including verbs no RFC names,
so a proxy can forward whatever the client sent instead of rejecting anything
not enumerated at registration time:

	r.ANY("/api/{rest...}", proxy)

WithPriority orders a route against the others that match the same path, ahead
of specificity, so a catch-all can deliberately shadow a more specific route:

	r.ANY("/api/{rest...}", maintenance, njia.WithPriority(-1))

Mount hands a whole subtree to one handler, which is how another router, a file
server or a debug endpoint is attached to a path. The prefix is not stripped:

	r.Mount("/debug/pprof", pprofHandler)

# Middleware ordering

Middleware is resolved when the table is compiled, not when a route is
registered, so Use is not positional: it covers everything in its scope
whatever the order. Router.Use and Group.Use behave alike, differing only in
what they cover, and a route or child group that already existed is wrapped
just as one created afterwards.

	api := r.Group("/api")
	v1 := api.Group("/v1")
	v1.GET("/orders", listOrders)
	api.Use(auth)              // covers /api/v1/orders too

To wrap only some routes, nest a group or attach the middleware to the route
with WithMiddleware, so the scope is visible in the structure rather than
dependent on where a line sits.

Applied outermost first: router middleware, then each enclosing group from the
outside in, then the route's own, then the handler.

Router middleware also wraps the requests no route serves — the 404, the 405 and
the CleanPath and RedirectTrailingSlash redirects — so a CORS middleware can
answer a preflight and an access log records misses. Group and route middleware
only wraps matched routes.

Package github.com/jkaninda/njia/muxcompat is a separate, drop-in replacement
for github.com/gorilla/mux, for projects migrating away from it.
*/
package njia

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
)

// Router dispatches requests to registered handlers. The zero value is an
// empty router ready to use, equivalent to one from New, and must not be copied
// after first use.
//
// A router is safe for concurrent use once it is serving. Registering routes
// while requests are in flight is not: use Swap, which validates a new table
// off to the side and installs it atomically.
type Router struct {
	// NotFound serves requests that matched no route. When nil,
	// http.NotFoundHandler is used.
	//
	// Router-level middleware wraps it, and MethodNotAllowed and the redirects
	// below, just as it wraps a route: an access log or a CORS middleware sees
	// every request. Group and route middleware does not, because a request
	// that matched no route belongs to no group.
	NotFound http.Handler

	// MethodNotAllowed serves requests whose path matched a route but whose
	// method did not. When nil, a handler writing 405 with an Allow header is
	// used.
	MethodNotAllowed http.Handler

	// CleanPath makes the router redirect a request whose path is not in
	// canonical form to the cleaned form: 301 for GET and HEAD, 308 for every
	// other method so that the method and body survive the redirect.
	CleanPath bool

	// RedirectTrailingSlash makes the router redirect "/x/" to "/x", and
	// "/x" to "/x/", when only the other form is registered, with the same
	// status codes as CleanPath.
	RedirectTrailingSlash bool

	// RouteInContext makes the router attach the matched route to the request
	// even when the route declares no parameters, so that RouteOf works for
	// static routes too. It costs one allocation per request; leave it off if
	// handlers do not need it.
	RouteInContext bool

	mu  sync.Mutex
	b   *Builder
	mw  []Middleware
	tbl atomic.Pointer[table]
}

// New returns an empty router. It is equivalent to declaring a zero Router.
func New() *Router {
	r := &Router{}
	r.adopt(newBuilder())
	return r
}

// adopt installs a builder and arranges for registration through it — whether
// through the router or through a group — to drop the compiled table.
func (r *Router) adopt(b *Builder) {
	b.invalidate = func() { r.tbl.Store(nil) }
	r.b = b
}

// builder returns the router's builder, creating it on first use so that a
// zero Router is as usable as one from New. The caller holds r.mu.
func (r *Router) builder() *Builder {
	if r.b == nil {
		r.adopt(newBuilder())
	}
	return r.b
}

//  registration

// Use appends router-level middleware. It applies to every route, including
// routes registered before the call, outermost first in registration order.
//
// Router middleware belongs to the router, not to its route table, so it keeps
// wrapping every route across Swap. Middleware meant for one table only is
// added with Builder.Use inside the Swap callback, and runs inside this.
func (r *Router) Use(mw ...Middleware) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mw = append(r.mw, mw...)
	r.tbl.Store(nil)
}

// Handle registers a handler for a method and pattern. It returns an error —
// never a panic — when the pattern is malformed, duplicates an existing route,
// or conflicts with one already registered.
func (r *Router) Handle(method, pattern string, h http.Handler, opts ...RouteOption) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.builder().Handle(method, pattern, h, opts...)
	r.tbl.Store(nil)
	return err
}

// HandleFunc registers a handler function for a method and pattern.
func (r *Router) HandleFunc(method, pattern string, h http.HandlerFunc, opts ...RouteOption) error {
	return r.Handle(method, pattern, h, opts...)
}

// GET registers a handler for GET requests.
func (r *Router) GET(pattern string, h http.Handler, opts ...RouteOption) error {
	return r.Handle(http.MethodGet, pattern, h, opts...)
}

// POST registers a handler for POST requests.
func (r *Router) POST(pattern string, h http.Handler, opts ...RouteOption) error {
	return r.Handle(http.MethodPost, pattern, h, opts...)
}

// PUT registers a handler for PUT requests.
func (r *Router) PUT(pattern string, h http.Handler, opts ...RouteOption) error {
	return r.Handle(http.MethodPut, pattern, h, opts...)
}

// PATCH registers a handler for PATCH requests.
func (r *Router) PATCH(pattern string, h http.Handler, opts ...RouteOption) error {
	return r.Handle(http.MethodPatch, pattern, h, opts...)
}

// DELETE registers a handler for DELETE requests.
func (r *Router) DELETE(pattern string, h http.Handler, opts ...RouteOption) error {
	return r.Handle(http.MethodDelete, pattern, h, opts...)
}

// HEAD registers a handler for HEAD requests.
func (r *Router) HEAD(pattern string, h http.Handler, opts ...RouteOption) error {
	return r.Handle(http.MethodHead, pattern, h, opts...)
}

// OPTIONS registers a handler for OPTIONS requests.
func (r *Router) OPTIONS(pattern string, h http.Handler, opts ...RouteOption) error {
	return r.Handle(http.MethodOptions, pattern, h, opts...)
}

// ANY registers a handler for every HTTP method, which is what a reverse proxy
// forwarding arbitrary verbs needs. See MethodAny.
func (r *Router) ANY(pattern string, h http.Handler, opts ...RouteOption) error {
	return r.Handle(MethodAny, pattern, h, opts...)
}

// Err returns the first registration error recorded on the router, so that a
// caller which ignored individual return values can still check the table as a
// whole before serving:
//
//	api := r.Group("/api/v1", auth)
//	api.GET("/orders", listOrders)
//	api.POST("/orders", createOrder)
//	if err := r.Err(); err != nil {
//	    log.Fatalf("route table: %v", err)
//	}
//
// It is the Router-level equivalent of Builder.Err.
func (r *Router) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.builder().Err()
}

// Errs returns every registration error recorded on the router.
func (r *Router) Errs() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.builder().Errs()
}

// Group returns a group that prefixes its patterns and wraps its handlers.
func (r *Router) Group(prefix string, mw ...Middleware) *Group {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.builder().Group(prefix, mw...)
}

// Host returns a group whose routes only answer on the given host patterns.
// See ValidateHost for the accepted forms.
func (r *Router) Host(patterns ...string) *Group {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.builder().Host(patterns...)
}

// Swap replaces the route table. build is called with a fresh, empty builder;
// if it returns an error, or if any registration inside it fails, the router's
// existing table is left untouched. Otherwise the new table is installed
// atomically and requests already in flight finish against the old one.
//
// This is the reload path: a route table assembled from user-supplied
// configuration can be rejected in full without taking the process down.
//
// Middleware added with Router.Use wraps the new table as it wrapped the old
// one; the builder starts with none of its own.
//
// A Group obtained from the router before a swap belongs to the replaced
// builder and registering on it afterwards has no effect on the live table.
// Build every group from the Builder the callback is given.
func (r *Router) Swap(build func(*Builder) error) error {
	nb := newBuilder()
	if err := build(nb); err != nil {
		return err
	}
	if err := nb.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.adopt(nb)
	r.install(nb.compile(r.mw))
	return nil
}

// Routes returns a snapshot of every registered route, in registration order.
// It is everything a documentation generator needs: the template as written,
// the parameters it declares with their constraints and positions, the
// handler, and any annotations attached with WithMeta.
func (r *Router) Routes() []RouteInfo {
	t := r.table()
	out := make([]RouteInfo, len(t.routes))
	for i, rt := range t.routes {
		out[i] = rt.info()
	}
	return out
}

// Route returns the route registered under name, or nil.
func (r *Router) Route(name string) *Route { return r.table().names[name] }

// table returns the compiled table, compiling it if registration has happened
// since the last compile.
func (r *Router) table() *table {
	if t := r.tbl.Load(); t != nil {
		return t
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.tbl.Load(); t != nil {
		return t
	}
	t := r.builder().compile(r.mw)
	r.install(t)
	return t
}

// install makes t the live table, first building the middleware chain its
// unmatched requests are answered through. The caller holds r.mu.
func (r *Router) install(t *table) {
	if len(t.mw) > 0 {
		t.unmatched = chain(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			r.resolveUnmatched(w, req, t)
		}), t.mw)
	}
	r.tbl.Store(t)
}

// Lookup returns the route that would serve req, without allocating and
// without capturing parameters.
func (r *Router) Lookup(req *http.Request) (*Route, bool) {
	var cbuf [8]*pathEntry
	var pbuf [8]PathParam
	t := r.table()
	host, port := t.authority(req)
	rt, _, _, _ := t.find(req.URL.Path, host, port, req.Method, cbuf[:0], pbuf[:0])
	return rt, rt != nil
}

// LookupInto returns the route that would serve req and appends its captured
// parameters to params. Supplying a params slice with spare capacity makes the
// call allocation free.
func (r *Router) LookupInto(req *http.Request, params []PathParam) (*Route, []PathParam, bool) {
	var cbuf [8]*pathEntry
	t := r.table()
	host, port := t.authority(req)
	rt, _, out, _ := t.find(req.URL.Path, host, port, req.Method, cbuf[:0], params)
	return rt, out, rt != nil
}

// ServeHTTP implements http.Handler.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	t := r.table()
	p := req.URL.Path

	if r.CleanPath {
		if cp := cleanPath(p); cp != p {
			if t.unmatched != nil {
				t.unmatched.ServeHTTP(w, req)
				return
			}
			redirect(w, req, cp)
			return
		}
	}

	host, port := t.authority(req)

	var cbuf [8]*pathEntry
	var pbuf [8]PathParam
	rt, h, params, pathMatched := t.find(p, host, port, req.Method, cbuf[:0], pbuf[:0])

	if rt == nil {
		if t.unmatched != nil {
			// Router middleware sees a miss as well as a hit. Nothing found
			// here can be carried through the chain without allocating, so the
			// answer is worked out again at its far end.
			t.unmatched.ServeHTTP(w, req)
			return
		}
		r.answerUnmatched(w, req, t, p, host, port, pathMatched)
		return
	}

	if len(params) > 0 || r.RouteInContext {
		req = withMatch(req, rt, params)
	}
	h.ServeHTTP(w, req)
}

// resolveUnmatched answers a request that no route serves, from inside the
// router's middleware. It decides afresh, on the request as the middleware
// passed it on, between a clean-path redirect, a trailing-slash redirect, a
// 405 and a 404. A miss is never turned into a match here: routes are chosen
// before middleware runs, and a route's chain already carries it.
func (r *Router) resolveUnmatched(w http.ResponseWriter, req *http.Request, t *table) {
	p := req.URL.Path
	if r.CleanPath {
		if cp := cleanPath(p); cp != p {
			redirect(w, req, cp)
			return
		}
	}
	host, port := t.authority(req)
	var cbuf [8]*pathEntry
	var pbuf [8]PathParam
	_, _, _, pathMatched := t.find(p, host, port, req.Method, cbuf[:0], pbuf[:0])
	r.answerUnmatched(w, req, t, p, host, port, pathMatched)
}

// answerUnmatched sends the trailing-slash redirect, 405 or 404 for a request
// no route serves. pathMatched reports that some route matched the path under
// another method.
func (r *Router) answerUnmatched(w http.ResponseWriter, req *http.Request, t *table, p, host, port string, pathMatched bool) {
	if r.RedirectTrailingSlash {
		if alt, ok := trailingSlashAlternative(p); ok {
			var cbuf [8]*pathEntry
			var pbuf [8]PathParam
			if _, _, _, altMatched := t.find(alt, host, port, req.Method, cbuf[:0], pbuf[:0]); altMatched {
				redirect(w, req, alt)
				return
			}
		}
	}
	if pathMatched {
		r.serveMethodNotAllowed(w, req, t, p, host, port)
		return
	}
	r.serveNotFound(w, req)
}

func (r *Router) serveNotFound(w http.ResponseWriter, req *http.Request) {
	if r.NotFound != nil {
		r.NotFound.ServeHTTP(w, req)
		return
	}
	http.NotFound(w, req)
}

func (r *Router) serveMethodNotAllowed(w http.ResponseWriter, req *http.Request, t *table, p, host, port string) {
	if allow := t.allowed(p, host, port); len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
	}
	if r.MethodNotAllowed != nil {
		r.MethodNotAllowed.ServeHTTP(w, req)
		return
	}
	http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
}

// redirect sends a permanent redirect to the same path-and-query with a
// different path.
//
// GET and HEAD get 301. Every other method gets 308, because clients replay a
// 301 as GET and drop the body, which would turn a POST to "/orders/" into a
// GET of "/orders".
//
// The target is built from the path and query alone. Copying the request URL
// would carry over the scheme and host of an absolute-form request target and
// send the client to another origin, and a path starting with "//" would be
// read as a protocol-relative URL, so leading slashes are collapsed to one.
func redirect(w http.ResponseWriter, req *http.Request, to string) {
	for len(to) > 1 && to[1] == '/' {
		to = to[1:]
	}
	u := url.URL{Path: to, RawQuery: req.URL.RawQuery}
	code := http.StatusMovedPermanently
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		code = http.StatusPermanentRedirect
	}
	http.Redirect(w, req, u.String(), code)
}

// trailingSlashAlternative returns the other trailing-slash form of p.
func trailingSlashAlternative(p string) (string, bool) {
	switch {
	case p == "" || p == "/":
		return "", false
	case strings.HasSuffix(p, "/"):
		return p[:len(p)-1], true
	default:
		return p + "/", true
	}
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

// String renders the route table, which is useful in tests and in start-up
// logs.
func (r *Router) String() string {
	var b strings.Builder
	for _, ri := range r.Routes() {
		fmt.Fprintf(&b, "%-7s ", ri.Method)
		if len(ri.Hosts) > 0 {
			fmt.Fprintf(&b, "[%s]", strings.Join(ri.Hosts, " "))
		}
		b.WriteString(ri.PathTemplate)
		if ri.Name != "" {
			fmt.Fprintf(&b, "  (%s)", ri.Name)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
