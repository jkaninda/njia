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
	"strings"
	"sync/atomic"

	"github.com/jkaninda/njia/internal/tree"
)

// indexThreshold is the number of routes below which the plain ordered scan is
// used. Indexing a handful of routes costs more than it saves.
const indexThreshold = 4

// atomicIndex holds the lookup accelerator for a router. It is built on first
// use after registration and dropped whenever a route is added, so that
// registration stays cheap and the request path stays lock free.
type atomicIndex struct {
	v atomic.Pointer[routeIndex]
}

func (a *atomicIndex) invalidate()         { a.v.Store(nil) }
func (a *atomicIndex) load() *routeIndex   { return a.v.Load() }
func (a *atomicIndex) store(i *routeIndex) { a.v.Store(i) }

// routeIndex splits a router's routes into a set that can be matched by a
// prefix tree and a set that must be evaluated in order, while preserving the
// registration order across both.
//
// A route qualifies as fast when its only matchers are a full path template
// whose variables are whole-segment wildcards with the default pattern, and at
// most one method filter, in that order. Everything else — regular expression
// constraints, path prefixes, hosts, queries, headers, schemes, custom
// matchers, subrouters, strict-slash handling — is slow.
type routeIndex struct {
	// routeCount is the number of routes the index was built from. A mismatch
	// means routes were added and the index must be rebuilt.
	routeCount int
	// useEncodedPath is the path form the index was built for.
	useEncodedPath bool

	// entries is parallel to Router.routes.
	entries []indexEntry

	// exact maps a fully static template to the routes registered under it.
	exact map[string][]int32
	// tree indexes templates that contain wildcards. Values are int32 route
	// positions.
	tree *tree.Tree[int32]

	// slowByFirstSegment buckets slow routes whose first matcher is a path or
	// prefix template beginning with a static segment. A request whose first
	// segment differs cannot match them, and skipping them is unobservable
	// because such a route fails on its very first matcher.
	slowByFirstSegment map[string][]int32
	// slowAlways holds slow routes that must be considered for every request.
	slowAlways []int32
}

// indexEntry is the per-route classification.
type indexEntry struct {
	// fast reports whether the route is matched through exact or tree lookup.
	fast bool
	// segs is the route's template in tree form, for parameter extraction.
	segs []tree.Segment
	// methods is the route's method filter. hasMethods distinguishes a route
	// with no method matcher at all from one built with Methods() and no
	// arguments, which matches nothing.
	methods    []string
	hasMethods bool
	// nparams is the number of wildcards in segs.
	nparams int
}

// buildIndex classifies the router's routes. It returns nil when indexing
// would not pay for itself.
func (r *Router) buildIndex() *routeIndex {
	if len(r.routes) < indexThreshold {
		return nil
	}
	idx := &routeIndex{
		routeCount:     len(r.routes),
		useEncodedPath: r.useEncodedPath,
		entries:        make([]indexEntry, len(r.routes)),
	}

	fastCount := 0
	for i, route := range r.routes {
		e, ok := classify(route, r.useEncodedPath)
		idx.entries[i] = e
		if !ok {
			idx.addSlow(int32(i), route)
			continue
		}
		fastCount++
		if e.nparams == 0 {
			if idx.exact == nil {
				idx.exact = make(map[string][]int32, len(r.routes))
			}
			key := route.regexp.path.tpl.Raw
			idx.exact[key] = append(idx.exact[key], int32(i))
			continue
		}
		if idx.tree == nil {
			idx.tree = &tree.Tree[int32]{}
		}
		if err := idx.tree.Insert(e.segs, int32(i)); err != nil {
			// A template the tree cannot represent falls back to the ordered
			// scan. Correctness never depends on the tree.
			idx.entries[i].fast = false
			idx.addSlow(int32(i), route)
			fastCount--
		}
	}
	if fastCount == 0 {
		return nil
	}
	return idx
}

// addSlow files a slow route into the bucket that can skip it safely.
func (idx *routeIndex) addSlow(i int32, route *Route) {
	if seg, ok := firstStaticSegment(route, idx.useEncodedPath); ok {
		if idx.slowByFirstSegment == nil {
			idx.slowByFirstSegment = make(map[string][]int32)
		}
		idx.slowByFirstSegment[seg] = append(idx.slowByFirstSegment[seg], i)
		return
	}
	idx.slowAlways = append(idx.slowAlways, i)
}

// firstStaticSegment returns the literal first path segment a slow route
// requires, when the route's very first matcher is its path or prefix template
// and that template starts with a static segment.
//
// Requiring the template to be the first matcher matters: a route that fails
// on its first matcher cannot clear a pending match error, so skipping it is
// invisible. A route whose host or method matcher runs first could clear one,
// and is never bucketed.
func firstStaticSegment(route *Route, encodedPath bool) (string, bool) {
	if route.err != nil || route.buildOnly || len(route.matchers) == 0 {
		return "", false
	}
	rr, ok := route.matchers[0].(*routeRegexp)
	if !ok || rr != route.regexp.path {
		return "", false
	}
	if rr.tpl.StrictSlash || rr.opts.useEncodedPath != encodedPath {
		return "", false
	}
	raw := rr.tpl.Raw
	if len(raw) == 0 || raw[0] != '/' {
		return "", false
	}
	seg := raw[1:]
	if i := strings.IndexByte(seg, '/'); i >= 0 {
		seg = seg[:i]
	} else if rr.kind == regexpKindPrefix {
		// A prefix template with a single segment is not segment-aligned:
		// PathPrefix("/static") matches "/staticky" as well as "/static/x", so
		// the request's first segment cannot be used to rule it out.
		return "", false
	}
	if seg == "" || strings.ContainsAny(seg, "{}") {
		return "", false
	}
	return seg, true
}

// classify decides whether a route can be matched by the tree, and computes
// its tree segments.
func classify(route *Route, routerEncodedPath bool) (indexEntry, bool) {
	var e indexEntry
	if route.err != nil || route.buildOnly {
		return e, false
	}
	rr := route.regexp.path
	if rr == nil || rr.kind != regexpKindPath {
		return e, false
	}
	if route.regexp.host != nil || len(route.regexp.queries) > 0 {
		return e, false
	}
	if rr.tpl.StrictSlash || rr.opts.useEncodedPath != routerEncodedPath {
		return e, false
	}
	if !rr.tpl.AllDefault {
		return e, false
	}

	switch len(route.matchers) {
	case 1:
		if route.matchers[0] != matcher(rr) {
			return e, false
		}
	case 2:
		if route.matchers[0] != matcher(rr) {
			return e, false
		}
		m, ok := route.matchers[1].(methodMatcher)
		if !ok {
			return e, false
		}
		e.methods = m
		e.hasMethods = true
	default:
		return e, false
	}

	segs, ok := fastSegments(rr.tpl.Raw)
	if !ok {
		return e, false
	}
	e.fast = true
	e.segs = segs
	for _, s := range segs {
		if s.Kind != tree.KindStatic {
			e.nparams++
		}
	}
	return e, true
}

// fastSegments converts a template to tree segments, reporting false when any
// variable is not a whole segment.
func fastSegments(raw string) ([]tree.Segment, bool) {
	if raw == "" {
		return nil, true
	}
	if raw[0] != '/' {
		return nil, false
	}
	parts := strings.Split(raw[1:], "/")
	segs := make([]tree.Segment, len(parts))
	for i, p := range parts {
		switch {
		case !strings.ContainsAny(p, "{}"):
			segs[i] = tree.Segment{Kind: tree.KindStatic, Text: p}
		case len(p) > 2 && p[0] == '{' && p[len(p)-1] == '}' &&
			!strings.ContainsAny(p[1:len(p)-1], "{}:"):
			segs[i] = tree.Segment{Kind: tree.KindParam, Text: p[1 : len(p)-1]}
		default:
			return nil, false
		}
	}
	return segs, true
}

// candidates appends the positions of every route that could match path, in
// ascending registration order.
func (idx *routeIndex) candidates(path string, dst []int32) []int32 {
	dst = append(dst, idx.exact[path]...)
	if idx.tree != nil {
		dst = idx.tree.Collect(path, dst)
	}
	dst = append(dst, idx.slowAlways...)
	if idx.slowByFirstSegment != nil {
		seg := path
		if len(seg) > 0 && seg[0] == '/' {
			seg = seg[1:]
		}
		if i := strings.IndexByte(seg, '/'); i >= 0 {
			seg = seg[:i]
		}
		dst = append(dst, idx.slowByFirstSegment[seg]...)
	}
	sortInt32(dst)
	return dst
}

// sortInt32 sorts a small slice in place. Insertion sort is used because the
// candidate set is tiny and the standard library's sort would allocate an
// interface value.
func sortInt32(a []int32) {
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 && a[j] > v {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
}
