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

// This file holds the compiled route table: the immutable snapshot a Builder
// freezes into, the per-pattern entries it is made of, and the lookup that runs
// against it. Router itself, in router.go, is the public surface over it.

package njia

import (
	"net/http"
	"slices"

	"github.com/jkaninda/njia/internal/tree"
)

// structure

// table is a compiled, immutable snapshot of a route table.
type table struct {
	static map[string]*pathEntry
	tree   *tree.Tree[*pathEntry]
	routes []*Route
	names  map[string]*Route
	// hasHosts reports that at least one route constrains the host. When it is
	// false the request's host is never even looked at.
	hasHosts bool
}

// pathEntry groups every route registered under one compiled path pattern.
//
// Routes that share a path but differ by host live here as separate variants,
// consulted in decreasing host specificity: a port-qualified exact host, then
// an exact host, then a single-label placeholder or a suffix wildcard, then the
// host-agnostic variant.
type pathEntry struct {
	pattern string
	segs    []tree.Segment
	score   uint64
	minSeq  int
	// nparams is how many parameters the path pattern declares.
	nparams int

	// plain is the variant that accepts any host, which is the only variant
	// most tables ever have.
	plain *hostVariant
	// exactPorted holds exact host patterns that also fix a port. They
	// outrank everything else, and there are normally none.
	exactPorted []*hostVariant
	// exact indexes port-agnostic exact hosts by their lower-cased name, so
	// that a gateway with hundreds of virtual hosts costs one map lookup.
	exact map[string]*hostVariant
	// wild holds placeholder and suffix-wildcard hosts, ordered by specificity
	// and then by registration.
	wild []*hostVariant
	// hasHosts reports that this entry has at least one host-constrained
	// variant.
	hasHosts bool
}

// hostVariant is the set of routes registered under one path pattern and one
// host pattern.
type hostVariant struct {
	// host is the compiled host constraint, or nil for the host-agnostic
	// variant.
	host *hostPattern
	// seq is the registration order of the first route filed under it.
	seq int

	byMethod map[string]*Route
	// served holds the fully wrapped handler for each method, including
	// router-level middleware.
	served map[string]http.Handler
}

// route returns the route for the method, falling back from HEAD to GET the
// way net/http itself does.
func (v *hostVariant) route(method string) (*Route, http.Handler) {
	if rt := v.byMethod[method]; rt != nil {
		return rt, v.served[method]
	}
	if method == http.MethodHead {
		if rt := v.byMethod[http.MethodGet]; rt != nil {
			return rt, v.served[http.MethodGet]
		}
	}
	return nil, nil
}

// route resolves a method against the entry's host-agnostic variant. It is the
// path taken by every table that does not use host matching.
func (e *pathEntry) route(method string) (*Route, http.Handler) {
	if e.plain == nil {
		return nil, nil
	}
	return e.plain.route(method)
}

// resolve selects the route for a host and method, appending any parameter the
// host pattern captures to params.
//
// The final result reports whether any variant accepted the host at all, which
// is what separates "this path is not served on this host" (404) from "it is,
// but not for this method" (405).
func (e *pathEntry) resolve(host, port, method string, params []PathParam) (*Route, http.Handler, []PathParam, bool) {
	if !e.hasHosts {
		rt, h := e.route(method)
		return rt, h, params, true
	}

	hostMatched := false
	base := len(params)

	for _, v := range e.exactPorted {
		out, ok := v.host.match(host, port, params)
		if !ok {
			params = params[:base]
			continue
		}
		hostMatched = true
		if rt, h := v.route(method); rt != nil {
			return rt, h, out, true
		}
		params = params[:base]
	}

	if v := e.lookupExact(host); v != nil {
		hostMatched = true
		if rt, h := v.route(method); rt != nil {
			return rt, h, params, true
		}
	}

	for _, v := range e.wild {
		out, ok := v.host.match(host, port, params)
		if !ok {
			params = params[:base]
			continue
		}
		hostMatched = true
		if rt, h := v.route(method); rt != nil {
			return rt, h, out, true
		}
		params = params[:base]
	}

	if e.plain != nil {
		hostMatched = true
		if rt, h := e.plain.route(method); rt != nil {
			return rt, h, params, true
		}
	}
	return nil, nil, params[:base], hostMatched
}

// lookupExact finds the exact-host variant for a request host. The host is
// tried as received first, because it is almost always already lower case, and
// only copied when it is not.
func (e *pathEntry) lookupExact(host string) *hostVariant {
	if e.exact == nil {
		return nil
	}
	if v := e.exact[host]; v != nil {
		return v
	}
	if hasUpper(host) {
		return e.exact[lower(host)]
	}
	return nil
}

// eachVariant calls fn for every variant of the entry.
func (e *pathEntry) eachVariant(fn func(*hostVariant)) {
	if e.plain != nil {
		fn(e.plain)
	}
	for _, v := range e.exactPorted {
		fn(v)
	}
	for _, v := range e.exact {
		fn(v)
	}
	for _, v := range e.wild {
		fn(v)
	}
}

// --- host variants ---------------------------------------------------------

// lookupVariant returns the entry's variant for a host pattern without
// creating one. Registration uses it to validate a route before anything is
// mutated, so that a rejected route leaves no trace: an empty variant left
// behind by a failed registration would make the router answer 405 where it
// should answer 404.
func (e *pathEntry) lookupVariant(p *hostPattern) *hostVariant {
	if p == nil || (p.kind == hostAny && p.name == "") {
		return e.plain
	}
	if key, ok := p.key(); ok {
		return e.exact[key]
	}
	bucket := e.wild
	if p.kind == hostExact {
		bucket = e.exactPorted
	}
	id := p.id()
	for _, v := range bucket {
		if v.host.id() == id {
			return v
		}
	}
	return nil
}

// variantFor returns the entry's variant for a host pattern, creating it if
// this is the first route registered under that host.
func (e *pathEntry) variantFor(p *hostPattern, seq int) *hostVariant {
	if p == nil || (p.kind == hostAny && p.name == "") {
		// An unconstrained route and an explicit "*" are the same thing.
		if e.plain == nil {
			e.plain = newHostVariant(nil, seq)
		}
		return e.plain
	}

	e.hasHosts = true
	if key, ok := p.key(); ok {
		if e.exact == nil {
			e.exact = make(map[string]*hostVariant, 4)
		}
		if v := e.exact[key]; v != nil {
			return v
		}
		v := newHostVariant(p, seq)
		e.exact[key] = v
		return v
	}

	bucket := &e.wild
	if p.kind == hostExact {
		// An exact host that also fixes a port outranks every port-agnostic
		// pattern, so it is kept in its own ordered bucket.
		bucket = &e.exactPorted
	}
	id := p.id()
	for _, v := range *bucket {
		if v.host.id() == id {
			return v
		}
	}
	v := newHostVariant(p, seq)
	*bucket = insertVariant(*bucket, v)
	return v
}

// newHostVariant returns an empty variant.
func newHostVariant(p *hostPattern, seq int) *hostVariant {
	return &hostVariant{
		host:     p,
		seq:      seq,
		byMethod: make(map[string]*Route, 2),
		served:   make(map[string]http.Handler, 2),
	}
}

// insertVariant places v in the slice, keeping it ordered by decreasing host
// specificity and then by registration order.
func insertVariant(vs []*hostVariant, v *hostVariant) []*hostVariant {
	i := len(vs)
	for i > 0 {
		prev := vs[i-1]
		if prev.host.score > v.host.score || (prev.host.score == v.host.score && prev.seq <= v.seq) {
			break
		}
		i--
	}
	vs = append(vs, nil)
	copy(vs[i+1:], vs[i:])
	vs[i] = v
	return vs
}

// hostPrefix renders a variant's host for an error message.
func hostPrefix(v *hostVariant) string {
	if v.host == nil {
		return ""
	}
	return v.host.raw + " "
}

//  lookup

// find selects the route that should serve the request. It reports whether any
// route matched the path, which is what distinguishes 405 from 404.
func (t *table) find(path, host, port, method string, cands []*pathEntry, params []PathParam) (*Route, http.Handler, []PathParam, bool) {
	// pbuf is the scratch space parameters are captured into. Whichever branch
	// below fills it copies what it captured into the caller's slice and then
	// returns, so no two uses of it are ever live at once.
	var pbuf [8]tree.Param

	// Fast path. A fully static pattern is the most specific thing that can
	// match a path, so an exact hit that also serves the host and method is
	// final.
	if e := t.static[path]; e != nil {
		if rt, h, out, _ := e.resolve(host, port, method, params); rt != nil {
			return rt, h, out, true
		}
		cands = append(cands, e)
	} else if t.tree.Len() > 0 {
		// Otherwise the tree's own depth-first walk already returns the most
		// specific match with its parameters captured, so neither a candidate
		// sort nor a second pass over the pattern is needed.
		e, tp, ok := t.tree.Lookup(path, pbuf[:0])
		if !ok {
			return nil, nil, params, false
		}
		if rt, h, out, _ := e.resolve(host, port, method, params); rt != nil {
			return rt, h, appendParams(out, tp), true
		}
	}

	// Slow path: the most specific match does not serve this host and method,
	// so a less specific one might.
	cands = t.tree.Collect(path, cands)
	if len(cands) == 0 {
		return nil, nil, params, false
	}
	if len(cands) > 1 {
		sortCandidates(cands)
	}
	pathMatched := false
	for _, e := range cands {
		rt, h, out, hostOK := e.resolve(host, port, method, params)
		if hostOK {
			// The path and host both matched something here, so a 405 is the
			// right answer even if no variant serves this method.
			pathMatched = true
		}
		if rt == nil {
			continue
		}
		if e.nparams > 0 {
			tp, ok := tree.Extract(e.segs, path, pbuf[:0])
			if !ok {
				continue
			}
			out = appendParams(out, tp)
		}
		return rt, h, out, true
	}
	return nil, nil, params, pathMatched
}

// appendParams appends captured tree parameters to a path-parameter slice.
func appendParams(dst []PathParam, src []tree.Param) []PathParam {
	for _, p := range src {
		dst = append(dst, PathParam{Name: p.Name, Value: p.Value})
	}
	return dst
}

// allowed returns the sorted set of methods any route matching path accepts.
func (t *table) allowed(path, host, port string) []string {
	var cbuf [8]*pathEntry
	cands := cbuf[:0]
	if e := t.static[path]; e != nil {
		cands = append(cands, e)
	}
	cands = t.tree.Collect(path, cands)

	// A method set is a handful of short strings, so it is collected into a
	// small slice and deduplicated by scanning it. That costs less than the map
	// it replaces, which had to be flattened again before it could be sorted.
	var mbuf [4]string
	out := mbuf[:0]
	var scratch [2]PathParam
	for _, e := range cands {
		e.eachVariant(func(v *hostVariant) {
			if v.host != nil {
				if _, ok := v.host.match(host, port, scratch[:0]); !ok {
					return
				}
			}
			for m := range v.byMethod {
				if !slices.Contains(out, m) {
					out = append(out, m)
				}
			}
		})
	}
	if slices.Contains(out, http.MethodGet) && !slices.Contains(out, http.MethodHead) {
		out = append(out, http.MethodHead)
	}
	slices.Sort(out)
	return out
}

// sortCandidates orders candidates by specificity, then by registration order.
// Insertion sort keeps the common case allocation free; candidate sets are
// tiny because they are all the patterns that matched one concrete path.
func sortCandidates(a []*pathEntry) {
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 {
			c := a[j]
			if c.score > v.score || (c.score == v.score && c.minSeq <= v.minSeq) {
				break
			}
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
}

// authority returns the host and port a request should be matched against, or
// two empty strings when no route in the table constrains the host.
func (t *table) authority(req *http.Request) (host, port string) {
	if !t.hasHosts {
		return "", ""
	}
	return requestAuthority(req)
}
