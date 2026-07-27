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
	"net/http"

	"github.com/jkaninda/njia/internal/tree"
)

// Middleware wraps a handler, returning the handler to invoke in its place.
type Middleware func(http.Handler) http.Handler

// MethodAny registers a handler for every HTTP method, including methods this
// package has no named helper for and methods that are not in any RFC.
//
// It is what a reverse proxy needs: a gateway forwards whatever verb the client
// sent — WebDAV's PROPFIND, a gRPC-Web POST, a vendor's custom verb — and
// decides for itself which ones a backend accepts. Enumerating the methods at
// registration time would make the router reject a verb the proxy would have
// been happy to forward.
//
// A method registered explicitly always wins over the wildcard, so a route can
// serve GET from one handler and everything else from another:
//
//	r.GET("/files/{path...}", readOnly)
//	r.ANY("/files/{path...}", proxy)
//
// A path served by a wildcard never answers 405, because there is no method it
// does not accept.
const MethodAny = "*"

// DefaultPriority is the priority a route has when none is given. Priorities
// are compared with lower first, so a route can be pulled ahead of the default
// with a negative value as well as pushed behind it with a positive one.
const DefaultPriority = 0

// Route is a registered method and pattern pair together with the handler that
// serves it.
type Route struct {
	// method is the HTTP method the route answers.
	method string
	// pattern is the template as written, for example "/users/{id}".
	pattern string
	// hosts are the host patterns the route is restricted to, as written. An
	// empty slice means the route answers on any host.
	hosts []string
	// hostPats are the compiled forms of hosts.
	hostPats []*hostPattern
	// name is an optional stable identifier.
	name string
	// handler is the fully wrapped handler, with group and route middleware
	// already applied.
	handler http.Handler
	// raw is the handler before middleware, reported by introspection.
	raw http.Handler
	// meta carries arbitrary user annotations, such as OpenAPI descriptions.
	meta map[string]any
	// params describes the pattern's parameters in order.
	params []ParamInfo
	// segs is the compiled pattern.
	segs []tree.Segment
	// seq is the registration order, used to break specificity ties.
	seq int
	// priority orders this route against the others that match the same path,
	// ahead of specificity. Lower sorts first; DefaultPriority is the middle.
	priority int
}

// Method returns the HTTP method the route answers.
func (r *Route) Method() string { return r.method }

// Pattern returns the route template as written.
func (r *Route) Pattern() string { return r.pattern }

// Hosts returns the host patterns the route is restricted to, as written. It
// returns nil when the route answers on any host.
func (r *Route) Hosts() []string {
	if len(r.hosts) == 0 {
		return nil
	}
	out := make([]string, len(r.hosts))
	copy(out, r.hosts)
	return out
}

// Name returns the route's name, or the empty string.
func (r *Route) Name() string { return r.name }

// Priority returns the route's matching priority. Lower sorts first, and
// DefaultPriority is what a route carries when none was set.
func (r *Route) Priority() int { return r.priority }

// Handler returns the handler the route was registered with, before
// middleware.
func (r *Route) Handler() http.Handler { return r.raw }

// Params returns the parameters the pattern declares, in order.
func (r *Route) Params() []ParamInfo {
	out := make([]ParamInfo, len(r.params))
	copy(out, r.params)
	return out
}

// Meta returns the annotation stored under key.
func (r *Route) Meta(key string) (any, bool) {
	v, ok := r.meta[key]
	return v, ok
}

// RouteInfo is a snapshot of a registered route, suitable for generating
// documentation such as an OpenAPI specification without reconstructing
// anything from regular expressions.
type RouteInfo struct {
	// Name is the route's name, or the empty string.
	Name string
	// Method is the HTTP method the route answers.
	Method string
	// PathTemplate is the pattern as written, for example "/users/{id}".
	PathTemplate string
	// Hosts are the host patterns the route is restricted to, as written. It is
	// nil when the route answers on any host.
	Hosts []string
	// Params describes the pattern's parameters in order.
	Params []ParamInfo
	// Handler is the handler the route was registered with, before middleware.
	Handler http.Handler
	// Meta carries the route's user annotations.
	Meta map[string]any
	// Priority is the route's matching priority, lower first. It is
	// DefaultPriority unless WithPriority set it.
	Priority int
}

// info renders the route for introspection.
func (r *Route) info() RouteInfo {
	ri := RouteInfo{
		Name:         r.name,
		Method:       r.method,
		PathTemplate: r.pattern,
		Hosts:        r.Hosts(),
		Handler:      r.raw,
		Params:       r.Params(),
		Priority:     r.priority,
	}
	if len(r.meta) > 0 {
		ri.Meta = make(map[string]any, len(r.meta))
		for k, v := range r.meta {
			ri.Meta[k] = v
		}
	}
	return ri
}

// RouteOption customises a route at registration time.
type RouteOption func(*Route)

// WithName gives the route a name, which must be unique within the router.
func WithName(name string) RouteOption {
	return func(r *Route) { r.name = name }
}

// WithPriority orders a route against the others whose patterns also match a
// request, ahead of specificity. Lower sorts first.
//
// Specificity is the right default: given "/api/v1/{rest...}" and
// "/api/{rest...}", the longer prefix is almost always the one meant to serve
// "/api/v1/x". A gateway loading routes from user configuration sometimes needs
// the opposite — a catch-all that deliberately shadows a more specific route
// during a migration, say — and specificity alone cannot express that.
//
//	r.ANY("/api/{rest...}", maintenance, njia.WithPriority(-1))
//	r.ANY("/api/v1/{rest...}", backend)
//
// Here the maintenance handler wins for every path under /api despite being the
// less specific pattern.
//
// Routes that share a path pattern share its priority: the lowest one any of
// them asks for applies to all, because priority orders patterns against each
// other and a pattern is matched before a method is chosen. Leaving priority
// unset everywhere costs nothing and keeps pure specificity ordering.
func WithPriority(p int) RouteOption {
	return func(r *Route) { r.priority = p }
}

// WithMeta attaches an arbitrary annotation to the route. Annotations are
// reported by Routes and are how a documentation generator carries summaries,
// tags or schemas alongside a route.
func WithMeta(key string, value any) RouteOption {
	return func(r *Route) {
		if r.meta == nil {
			r.meta = make(map[string]any, 2)
		}
		r.meta[key] = value
	}
}

// Ranks used by specificity, in increasing order of how specific a segment is.
// rankEnd sits between a catch-all and a real segment: a pattern that has run
// out of segments is more specific than one that swallows the rest of the path,
// and less specific than one that constrains the next segment.
//
// rankConstrained is the slot a wildcard carrying a value constraint occupies.
// No pattern this package parses produces one today — the tree still supports
// the constraint, and this reserves its place in the ordering.
const (
	rankCatchAll    uint64 = 1
	rankEnd         uint64 = 2
	rankParam       uint64 = 3
	rankConstrained uint64 = 4
	rankStatic      uint64 = 5

	// specBits is how many bits each position takes, and specDepth how many
	// positions are compared. Two patterns identical for this many segments are
	// ranked equal and fall back to registration order.
	specBits  = 3
	specDepth = 20
)

// specificity scores a compiled pattern so that, among the patterns that match
// one concrete path, the most specific one sorts first.
//
// Positions are compared left to right, so the first place two patterns differ
// decides: a static segment beats a constrained wildcard, which beats a bare
// wildcard, which beats the end of the pattern, which beats a catch-all. That
// last step is what makes "/api" win over "/api/{rest...}" for the request
// "/api"; comparing whole scores rather than positions would let the longer
// pattern win purely for being longer.
func specificity(segs []tree.Segment) uint64 {
	var score uint64
	for i := 0; i < specDepth; i++ {
		var d uint64
		switch {
		case i >= len(segs):
			d = rankEnd
		case segs[i].Kind == tree.KindStatic:
			d = rankStatic
		case segs[i].Kind == tree.KindCatchAll:
			d = rankCatchAll
		case segs[i].Check != nil:
			d = rankConstrained
		default:
			d = rankParam
		}
		score = score<<specBits | d
		if i < len(segs) && segs[i].Kind == tree.KindCatchAll {
			segs = segs[:i+1]
		}
	}
	return score
}
