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

// Benchmarks for the two features a reverse proxy uses — wildcard methods and
// explicit priority — measured against the same table registered without them,
// so each row is read as the cost of the feature rather than as a number on its
// own.
//
// These are njia-native only and so are not part of the cross-engine grid:
// gorilla has no equivalent of either, and comparing against an engine that
// does something different would say nothing.

package bench

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/jkaninda/njia"
)

// buildMethodTable registers the standard route set under one method, which is
// njia.MethodAny for the wildcard variants.
func buildMethodTable(rs routeSet, method string) http.Handler {
	r := njia.New()
	for _, p := range rs.staticPaths {
		must(r.Handle(method, p, http.HandlerFunc(noop)))
	}
	for _, p := range rs.paramPaths {
		must(r.Handle(method, p, http.HandlerFunc(noop)))
	}
	must(r.Handle(method, rs.deepPath, http.HandlerFunc(noop)))
	return r
}

// BenchmarkGateway_Wildcard measures what dispatching through MethodAny costs
// against the same table registered for GET.
//
// The wildcard is consulted only after an exact method lookup misses, so the
// "explicit" rows should be unchanged from a plain GET table and the
// "wildcard" rows carry one extra map lookup.
func BenchmarkGateway_Wildcard(b *testing.B) {
	for _, size := range sizes {
		rs := makeRoutes(size)

		// Baseline: an ordinary GET table answering GET.
		b.Run(fmt.Sprintf("explicit-get/%d", size), func(b *testing.B) {
			benchServe(b, buildMethodTable(rs, http.MethodGet), http.MethodGet, rs.staticHit)
		})
		// A wildcard table answering GET: the exact lookup misses and the
		// wildcard entry serves it.
		b.Run(fmt.Sprintf("wildcard-get/%d", size), func(b *testing.B) {
			benchServe(b, buildMethodTable(rs, njia.MethodAny), http.MethodGet, rs.staticHit)
		})
		// A wildcard table answering a verb no helper names, which is the case
		// a gateway forwarding arbitrary methods actually hits.
		b.Run(fmt.Sprintf("wildcard-propfind/%d", size), func(b *testing.B) {
			benchServe(b, buildMethodTable(rs, njia.MethodAny), "PROPFIND", rs.staticHit)
		})
	}
}

// buildPriorityTable registers the standard route set, optionally giving one
// route a non-default priority.
//
// One priority anywhere in the table disables the lookup fast paths for the
// whole table, so this is deliberately the cheapest possible way to turn the
// feature on: it isolates the cost of the slow path from the cost of having
// many prioritised routes.
func buildPriorityTable(rs routeSet, withPriority bool) http.Handler {
	r := njia.New()
	var opts []njia.RouteOption
	if withPriority {
		opts = append(opts, njia.WithPriority(-1))
	}
	for _, p := range rs.staticPaths {
		must(r.GET(p, http.HandlerFunc(noop)))
	}
	for _, p := range rs.paramPaths {
		must(r.GET(p, http.HandlerFunc(noop)))
	}
	// Only the deep route is prioritised; every other route is ordinary.
	must(r.GET(rs.deepPath, http.HandlerFunc(noop), opts...))
	return r
}

// BenchmarkGateway_Priority measures what a table pays for carrying any
// priority at all, on requests that do not themselves use the feature.
//
// This is the number that decides whether priority is safe to leave available:
// the static and parameter hits below are ordinary requests, and the delta
// between the two rows is what the fast-path gate costs them.
func BenchmarkGateway_Priority(b *testing.B) {
	for _, size := range sizes {
		rs := makeRoutes(size)

		b.Run(fmt.Sprintf("default-static/%d", size), func(b *testing.B) {
			benchServe(b, buildPriorityTable(rs, false), http.MethodGet, rs.staticHit)
		})
		b.Run(fmt.Sprintf("priority-static/%d", size), func(b *testing.B) {
			benchServe(b, buildPriorityTable(rs, true), http.MethodGet, rs.staticHit)
		})
		b.Run(fmt.Sprintf("default-param/%d", size), func(b *testing.B) {
			benchServe(b, buildPriorityTable(rs, false), http.MethodGet, rs.paramHit)
		})
		b.Run(fmt.Sprintf("priority-param/%d", size), func(b *testing.B) {
			benchServe(b, buildPriorityTable(rs, true), http.MethodGet, rs.paramHit)
		})
	}
}

// buildProxyTable registers routes the way an API gateway does: every route is
// a path plus a catch-all for everything beneath it, served for every method.
func buildProxyTable(n int) (http.Handler, string) {
	r := njia.New()
	for i := 0; i < n; i++ {
		base := fmt.Sprintf("/svc/%d", i)
		must(r.ANY(base, http.HandlerFunc(noop)))
		must(r.ANY(base+"/{rest...}", http.HandlerFunc(noop)))
	}
	return r, fmt.Sprintf("/svc/%d/v1/orders/4242", n-1)
}

// BenchmarkGateway_ProxyTable measures the table shape a gateway actually
// builds — prefix plus catch-all, every method — rather than the handler-per-
// endpoint shape the rest of the grid uses.
func BenchmarkGateway_ProxyTable(b *testing.B) {
	for _, size := range sizes {
		h, target := buildProxyTable(size)
		b.Run(fmt.Sprintf("catchall-hit/%d", size), func(b *testing.B) {
			benchServe(b, h, http.MethodGet, target)
		})
		b.Run(fmt.Sprintf("catchall-propfind/%d", size), func(b *testing.B) {
			benchServe(b, h, "PROPFIND", target)
		})
	}
}
