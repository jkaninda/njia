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

	"github.com/jkaninda/njia/internal/tree"
)

// Builder accumulates a route table. It is what Swap hands to its callback,
// and it is also the object a Router registers into.
//
// Every registration is validated as it happens, so an invalid table is
// rejected before it can be installed.
type Builder struct {
	tree    *tree.Tree[*pathEntry]
	static  map[string]*pathEntry
	byKey   map[string]*pathEntry
	entries []*pathEntry
	routes  []*Route
	names   map[string]*Route
	mw      []Middleware
	seq     int
	errs    []error

	// invalidate is called after every successful registration so that the
	// router owning this builder drops its compiled table. Registering through
	// a Group reaches the builder directly, so without this a route added after
	// the first request would be visible in the table but have no handler chain
	// compiled for it.
	invalidate func()
}

// NewBuilder returns an empty builder. A table can be assembled and fully
// validated with one, then installed on a router with Swap.
func NewBuilder() *Builder { return newBuilder() }

func newBuilder() *Builder {
	return &Builder{
		tree:   &tree.Tree[*pathEntry]{},
		static: make(map[string]*pathEntry),
		byKey:  make(map[string]*pathEntry),
		names:  make(map[string]*Route),
	}
}

// Use appends router-level middleware, applied outermost first to every route
// in the table.
func (b *Builder) Use(mw ...Middleware) { b.mw = append(b.mw, mw...) }

// Err returns the first registration error recorded on the builder, so that a
// caller which ignored individual return values can still check the table as a
// whole.
func (b *Builder) Err() error {
	if len(b.errs) == 0 {
		return nil
	}
	return b.errs[0]
}

// Errs returns every registration error recorded on the builder.
func (b *Builder) Errs() []error { return b.errs }

// Group returns a group rooted at the builder.
func (b *Builder) Group(prefix string, mw ...Middleware) *Group {
	return (&Group{b: b}).Group(prefix, mw...)
}

// Host returns a group rooted at the builder whose routes only answer on the
// given host patterns.
func (b *Builder) Host(patterns ...string) *Group {
	return (&Group{b: b}).Host(patterns...)
}

// Handle registers a handler for a method and pattern.
func (b *Builder) Handle(method, pattern string, h http.Handler, opts ...RouteOption) error {
	return (&Group{b: b}).Handle(method, pattern, h, opts...)
}

// HandleFunc registers a handler function for a method and pattern.
func (b *Builder) HandleFunc(method, pattern string, h http.HandlerFunc, opts ...RouteOption) error {
	return b.Handle(method, pattern, h, opts...)
}

// GET registers a handler for GET requests.
func (b *Builder) GET(pattern string, h http.Handler, opts ...RouteOption) error {
	return b.Handle(http.MethodGet, pattern, h, opts...)
}

// POST registers a handler for POST requests.
func (b *Builder) POST(pattern string, h http.Handler, opts ...RouteOption) error {
	return b.Handle(http.MethodPost, pattern, h, opts...)
}

// PUT registers a handler for PUT requests.
func (b *Builder) PUT(pattern string, h http.Handler, opts ...RouteOption) error {
	return b.Handle(http.MethodPut, pattern, h, opts...)
}

// PATCH registers a handler for PATCH requests.
func (b *Builder) PATCH(pattern string, h http.Handler, opts ...RouteOption) error {
	return b.Handle(http.MethodPatch, pattern, h, opts...)
}

// DELETE registers a handler for DELETE requests.
func (b *Builder) DELETE(pattern string, h http.Handler, opts ...RouteOption) error {
	return b.Handle(http.MethodDelete, pattern, h, opts...)
}

// HEAD registers a handler for HEAD requests.
func (b *Builder) HEAD(pattern string, h http.Handler, opts ...RouteOption) error {
	return b.Handle(http.MethodHead, pattern, h, opts...)
}

// OPTIONS registers a handler for OPTIONS requests.
func (b *Builder) OPTIONS(pattern string, h http.Handler, opts ...RouteOption) error {
	return b.Handle(http.MethodOptions, pattern, h, opts...)
}

// ANY registers a handler for every HTTP method. See MethodAny.
func (b *Builder) ANY(pattern string, h http.Handler, opts ...RouteOption) error {
	return b.Handle(MethodAny, pattern, h, opts...)
}

// Group is a path prefix and a middleware chain that routes are registered
// against.
//
// Middleware ordering is fixed and tested: router-level middleware is
// outermost, then each enclosing group's middleware from outer to inner, then
// the route's own middleware, then the handler.
type Group struct {
	b      *Builder
	parent *Group
	prefix string
	// own is the middleware added to this group alone. An enclosing group's
	// middleware is reached through parent rather than copied in, so that Use
	// on an ancestor is visible to a group that already exists.
	own   []Middleware
	hosts []string
}

// chainTo appends the group's middleware to dst, outermost first: every
// ancestor from the outside in, then the group's own.
//
// It is resolved when the table is compiled rather than when a route is
// registered, which is what makes Use order-independent.
func (g *Group) chainTo(dst []Middleware) []Middleware {
	if g == nil {
		return dst
	}
	dst = g.parent.chainTo(dst)
	return append(dst, g.own...)
}

// Group returns a nested group.
//
// The child records its parent rather than copying its middleware, so
// middleware added to any enclosing group afterwards still reaches this child's
// routes.
func (g *Group) Group(prefix string, mw ...Middleware) *Group {
	return &Group{
		b:      g.b,
		parent: g,
		prefix: joinPrefix(g.prefix, prefix),
		hosts:  g.hosts,
		own:    append([]Middleware(nil), mw...),
	}
}

// Use appends middleware to this group and everything nested inside it.
//
// Like Router.Use, it applies to every route in its scope whatever the order:
// routes registered before the call are covered as well as routes registered
// after, and so are child groups created before it. Middleware is resolved when
// the table is compiled, not when a route is registered.
//
//	api := r.Group("/api")
//	v1 := api.Group("/v1")
//	v1.GET("/orders", listOrders)
//	api.Use(auth)               // covers /api/v1/orders too
//
// To scope middleware to some routes and not others, nest a group or attach it
// to the route with WithMiddleware. Where the call sits among the registrations
// does not change what it covers.
func (g *Group) Use(mw ...Middleware) {
	g.own = append(g.own, mw...)
	if g.b != nil && g.b.invalidate != nil {
		// The compiled table holds handler chains built from this middleware,
		// so it has to be rebuilt before the next request is served.
		g.b.invalidate()
	}
}

// Host returns a group whose routes only answer on the given host patterns.
//
// The patterns replace, rather than narrow, any host constraint the group
// already carried: a nested Host call declares outright which hosts its subtree
// serves. A route matches when any one of the patterns matches.
//
//	gw := r.Host("api.example.com", "*.api.example.com")
//	gw.GET("/orders/{id}", getOrder)
//
// See ParseHostPattern for the accepted forms.
func (g *Group) Host(patterns ...string) *Group {
	return &Group{
		b:      g.b,
		parent: g,
		prefix: g.prefix,
		hosts:  append([]string(nil), patterns...),
	}
}

// Hosts returns the host patterns the group restricts its routes to.
func (g *Group) Hosts() []string {
	if len(g.hosts) == 0 {
		return nil
	}
	out := make([]string, len(g.hosts))
	copy(out, g.hosts)
	return out
}

// Prefix returns the group's accumulated path prefix.
func (g *Group) Prefix() string { return g.prefix }

// GET registers a handler for GET requests.
func (g *Group) GET(pattern string, h http.Handler, opts ...RouteOption) error {
	return g.Handle(http.MethodGet, pattern, h, opts...)
}

// POST registers a handler for POST requests.
func (g *Group) POST(pattern string, h http.Handler, opts ...RouteOption) error {
	return g.Handle(http.MethodPost, pattern, h, opts...)
}

// PUT registers a handler for PUT requests.
func (g *Group) PUT(pattern string, h http.Handler, opts ...RouteOption) error {
	return g.Handle(http.MethodPut, pattern, h, opts...)
}

// PATCH registers a handler for PATCH requests.
func (g *Group) PATCH(pattern string, h http.Handler, opts ...RouteOption) error {
	return g.Handle(http.MethodPatch, pattern, h, opts...)
}

// DELETE registers a handler for DELETE requests.
func (g *Group) DELETE(pattern string, h http.Handler, opts ...RouteOption) error {
	return g.Handle(http.MethodDelete, pattern, h, opts...)
}

// HEAD registers a handler for HEAD requests.
func (g *Group) HEAD(pattern string, h http.Handler, opts ...RouteOption) error {
	return g.Handle(http.MethodHead, pattern, h, opts...)
}

// OPTIONS registers a handler for OPTIONS requests.
func (g *Group) OPTIONS(pattern string, h http.Handler, opts ...RouteOption) error {
	return g.Handle(http.MethodOptions, pattern, h, opts...)
}

// ANY registers a handler for every HTTP method. See MethodAny.
func (g *Group) ANY(pattern string, h http.Handler, opts ...RouteOption) error {
	return g.Handle(MethodAny, pattern, h, opts...)
}

// HandleFunc registers a handler function for a method and pattern.
func (g *Group) HandleFunc(method, pattern string, h http.HandlerFunc, opts ...RouteOption) error {
	return g.Handle(method, pattern, h, opts...)
}

// WithHost restricts a single route to the given host patterns, overriding
// whatever host constraint its group carried. See ParseHostPattern for the
// accepted forms.
func WithHost(patterns ...string) RouteOption {
	return func(r *Route) {
		r.hosts = append(r.hosts[:0:0], patterns...)
	}
}

// WithMiddleware returns a RouteOption that wraps only this route, inside any
// group middleware.
func WithMiddleware(mw ...Middleware) RouteOption {
	return func(r *Route) {
		r.handler = chain(r.handler, mw)
	}
}

// Handle registers a handler for a method and pattern within the group.
func (g *Group) Handle(method, pattern string, h http.Handler, opts ...RouteOption) error {
	err := g.handle(method, pattern, h, opts...)
	if err != nil {
		g.b.errs = append(g.b.errs, err)
	}
	return err
}

func (g *Group) handle(method, pattern string, h http.Handler, opts ...RouteOption) error {
	full := joinPrefix(g.prefix, pattern)
	if method == "" {
		return routeErr(method, full, ErrEmptyMethod)
	}
	if h == nil {
		return routeErr(method, full, ErrNoHandler)
	}
	method = strings.ToUpper(method)

	segs, params, err := parsePattern(full)
	if err != nil {
		return routeErr(method, full, err)
	}

	rt := &Route{
		method:  method,
		pattern: full,
		hosts:   g.hosts,
		raw:     h,
		handler: h,
		params:  params,
		segs:    segs,
		seq:     g.b.seq,
	}
	for _, o := range opts {
		o(rt)
	}

	hostParam, err := rt.compileHosts()
	if err != nil {
		return routeErr(method, full, err)
	}
	if hostParam != "" {
		for _, p := range params {
			if p.Name == hostParam {
				return routeErr(method, full, fmt.Errorf(
					"%w: %q is captured by both the host and the path", ErrParamConflict, hostParam))
			}
		}
		// A host parameter is reported and captured before every path
		// parameter, so that ParamAt indexes line up with Route.Params.
		rt.params = append([]ParamInfo{{Name: hostParam, Position: -1, InHost: true}}, params...)
	}
	// The group's middleware is not applied here: it is resolved at compile
	// time, so that Use on this group or any enclosing one reaches this route
	// whenever it is called. The route keeps a reference to the group it was
	// registered on, which is what compile walks.
	rt.group = g

	if rt.name != "" {
		if _, dup := g.b.names[rt.name]; dup {
			return routeErr(method, full, fmt.Errorf("%w: %q", ErrDuplicateName, rt.name))
		}
	}

	// One route may be registered under several host patterns; each becomes a
	// variant of the path, and a duplicate is only a duplicate within one. A
	// route with no host constraint has a single, host-agnostic variant.
	hostPats := rt.hostPats
	if len(hostPats) == 0 {
		hostPats = []*hostPattern{nil}
	}

	// Everything below this point is validated before anything is mutated, so
	// that a rejected registration leaves the table exactly as it was.
	key := patternKey(segs)
	entry := g.b.byKey[key]
	if entry != nil {
		// The key ignores parameter names, so two patterns that differ only in
		// how they name the same position land on the same entry. That is a
		// conflict, not a duplicate: it would make the captured name depend on
		// which route happened to be registered first.
		if err := sameParamNames(entry.segs, segs); err != nil {
			return routeErr(method, full, err)
		}
		for _, hp := range hostPats {
			v := entry.lookupVariant(hp)
			if v == nil {
				continue
			}
			if _, dup := v.byMethod[method]; dup {
				return routeErr(method, full, fmt.Errorf("%w: %s %s%s already registered",
					ErrDuplicateRoute, method, hostPrefix(v), full))
			}
		}
	}

	if entry == nil {
		entry = &pathEntry{
			pattern:  full,
			segs:     segs,
			score:    specificity(segs),
			minSeq:   rt.seq,
			priority: rt.priority,
			nparams:  len(params),
		}
		if len(params) == 0 {
			g.b.static[full] = entry
		} else if err := g.b.tree.Insert(segs, entry); err != nil {
			return routeErr(method, full, translateTreeErr(err))
		}
		g.b.byKey[key] = entry
		g.b.entries = append(g.b.entries, entry)
	} else if rt.priority < entry.priority {
		// Ordering picks a pattern before it picks a method, so every route
		// filed under a pattern shares one priority: the most urgent asked for.
		entry.priority = rt.priority
	}
	for _, hp := range hostPats {
		entry.variantFor(hp, rt.seq).byMethod[method] = rt
	}

	g.b.routes = append(g.b.routes, rt)
	if rt.name != "" {
		g.b.names[rt.name] = rt
	}
	g.b.seq++
	if g.b.invalidate != nil {
		g.b.invalidate()
	}
	return nil
}

// sameParamNames reports an error when two compiled patterns name the same
// position differently.
func sameParamNames(existing, incoming []tree.Segment) error {
	for i := range existing {
		if existing[i].Kind == tree.KindStatic {
			continue
		}
		if existing[i].Text != incoming[i].Text {
			return fmt.Errorf("%w: position %d is named %q here and %q in an existing route",
				ErrParamConflict, i, incoming[i].Text, existing[i].Text)
		}
	}
	return nil
}

// translateTreeErr maps an internal tree error onto this package's sentinels.
func translateTreeErr(err error) error {
	switch {
	case strings.Contains(err.Error(), "catch-all"):
		return fmt.Errorf("%w: %v", ErrCatchAllPosition, err)
	default:
		return fmt.Errorf("%w: %v", ErrParamConflict, err)
	}
}

// patternKey renders a compiled pattern into a string that is equal for two
// patterns the tree cannot tell apart.
func patternKey(segs []tree.Segment) string {
	var b strings.Builder
	for _, s := range segs {
		switch s.Kind {
		case tree.KindStatic:
			b.WriteString("s")
			b.WriteString(s.Text)
		case tree.KindParam:
			b.WriteString("p:")
			b.WriteString(s.Type)
		default:
			b.WriteString("c:")
			b.WriteString(s.Type)
		}
		b.WriteByte('/')
	}
	return b.String()
}

// joinPrefix concatenates a group prefix and a pattern.
func joinPrefix(prefix, pattern string) string {
	switch {
	case prefix == "":
		return pattern
	case pattern == "" || pattern == "/":
		return prefix
	case strings.HasSuffix(prefix, "/") && strings.HasPrefix(pattern, "/"):
		return prefix + pattern[1:]
	case !strings.HasSuffix(prefix, "/") && !strings.HasPrefix(pattern, "/"):
		return prefix + "/" + pattern
	default:
		return prefix + pattern
	}
}

// chain wraps h with mw, outermost first.
func chain(h http.Handler, mw []Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// compile freezes the builder into a table, applying router-level middleware.
func (b *Builder) compile() *table {
	hasHosts := false
	hasPriority := false
	// buf is reused across routes to assemble each handler's middleware, so
	// compiling a large table does not allocate one slice per route.
	var buf []Middleware
	for _, e := range b.entries {
		e.eachVariant(func(v *hostVariant) {
			for m, rt := range v.byMethod {
				// Outermost first: router middleware, then each enclosing group
				// from the outside in, then the group's own. The route's own
				// middleware was already applied by WithMiddleware, so it ends
				// up inside all of them.
				buf = append(buf[:0], b.mw...)
				buf = rt.group.chainTo(buf)
				v.served[m] = chain(rt.handler, buf)
			}
		})
		if e.hasHosts {
			hasHosts = true
		}
		if e.priority != DefaultPriority {
			hasPriority = true
		}
	}
	return &table{
		static:      b.static,
		tree:        b.tree,
		routes:      b.routes,
		names:       b.names,
		hasHosts:    hasHosts,
		hasPriority: hasPriority,
	}
}
