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

// This file holds Mount, which hands a whole subtree of paths to one handler.

package njia

import (
	"net/http"
	"strings"
)

// MountParam is the name of the parameter a mounted subtree captures the
// remainder of the path into. It is reported by Route.Params like any other, so
// a mounted handler can read the unmatched remainder with Param(r, MountParam)
// when it wants it.
//
// Because it is a fixed name, mounting under a prefix and separately
// registering a differently named catch-all at the same position conflict:
//
//	r.Mount("/admin", h)              // registers /admin/{mount...}
//	r.GET("/admin/{files...}", other) // ErrParamConflict: position already named
//
// Registering a plain {name} parameter there is fine; only two catch-alls at
// one position collide.
const MountParam = "mount"

// mountPatterns returns the two patterns that together cover a prefix and
// everything beneath it.
//
// Two are needed because a catch-all matches at least one segment: "/admin"
// itself would not match "/admin/{mount...}".
func mountPatterns(prefix string) (self, sub string) {
	p := strings.TrimRight(prefix, "/")
	if p != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if p == "" {
		return "/", "/{" + MountParam + "...}"
	}
	return p, p + "/{" + MountParam + "...}"
}

// mount registers h for a prefix and everything under it, for every method.
func mount(reg func(method, pattern string, h http.Handler, opts ...RouteOption) error,
	prefix string, h http.Handler, opts ...RouteOption) error {
	self, sub := mountPatterns(prefix)
	if err := reg(MethodAny, self, h, opts...); err != nil {
		return err
	}
	return reg(MethodAny, sub, h, opts...)
}

// Mount hands every request at or below a prefix to one handler, for every HTTP
// method. It is how another router, a file server, a debug endpoint or any
// third-party http.Handler is attached to a path:
//
//	r.Mount("/debug/pprof", pprofHandler)
//	r.Mount("/static", http.FileServer(http.Dir("public")))
//	r.Mount("/legacy", oldRouter)
//
// Both the prefix itself and its subtree are covered, so "/static" and
// "/static/css/app.css" both reach the handler.
//
// The prefix is NOT stripped: the handler sees the request path as it arrived.
// A gateway proxying to a backend needs the full path, and a handler that wants
// the prefix removed can say so explicitly, which reads better than a routing
// rule that silently rewrites:
//
//	r.Mount("/static", http.StripPrefix("/static", fs))
//
// Matching is on segment boundaries, so mounting "/api" does not capture
// "/apiary". A route registered under a mounted prefix still wins on
// specificity, which is what makes carving an exception out of a mount work:
//
//	r.Mount("/api", proxy)
//	r.GET("/api/health", localHealth)  // served locally, not proxied
//
// See MountParam for the one pattern that conflicts with a mount.
func (r *Router) Mount(prefix string, h http.Handler, opts ...RouteOption) error {
	return mount(r.Handle, prefix, h, opts...)
}

// Mount hands every request at or below a prefix, resolved against the group's
// own prefix, to one handler. See Router.Mount.
func (g *Group) Mount(prefix string, h http.Handler, opts ...RouteOption) error {
	return mount(g.Handle, prefix, h, opts...)
}

// Mount hands every request at or below a prefix to one handler. See
// Router.Mount.
func (b *Builder) Mount(prefix string, h http.Handler, opts ...RouteOption) error {
	return mount(b.Handle, prefix, h, opts...)
}
