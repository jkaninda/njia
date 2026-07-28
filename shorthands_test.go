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

// Tests for the per-method shorthands on Router, Group and Builder.
//
// Each one is a single delegation to Handle with a method constant, so there is
// no logic here to get wrong — only the constant. That is exactly what makes
// them worth testing: PATCH written as http.MethodPut compiles, reads correctly
// at a glance, and would be caught by nothing else. The assertion is therefore
// on the method the route actually registered under, read back through
// introspection, rather than on anything the handler does.

package njia_test

import (
	"net/http"
	"testing"

	"github.com/jkaninda/njia"
)

// verbShorthand names one method helper and binds it on each of the three
// receivers that offer it.
type verbShorthand struct {
	name   string
	method string
	router func(*njia.Router, string, http.Handler) error
	group  func(*njia.Group, string, http.Handler) error
	build  func(*njia.Builder, string, http.Handler) error
}

var verbShorthands = []verbShorthand{
	{"GET", http.MethodGet,
		func(r *njia.Router, p string, h http.Handler) error { return r.GET(p, h) },
		func(g *njia.Group, p string, h http.Handler) error { return g.GET(p, h) },
		func(b *njia.Builder, p string, h http.Handler) error { return b.GET(p, h) }},
	{"POST", http.MethodPost,
		func(r *njia.Router, p string, h http.Handler) error { return r.POST(p, h) },
		func(g *njia.Group, p string, h http.Handler) error { return g.POST(p, h) },
		func(b *njia.Builder, p string, h http.Handler) error { return b.POST(p, h) }},
	{"PUT", http.MethodPut,
		func(r *njia.Router, p string, h http.Handler) error { return r.PUT(p, h) },
		func(g *njia.Group, p string, h http.Handler) error { return g.PUT(p, h) },
		func(b *njia.Builder, p string, h http.Handler) error { return b.PUT(p, h) }},
	{"PATCH", http.MethodPatch,
		func(r *njia.Router, p string, h http.Handler) error { return r.PATCH(p, h) },
		func(g *njia.Group, p string, h http.Handler) error { return g.PATCH(p, h) },
		func(b *njia.Builder, p string, h http.Handler) error { return b.PATCH(p, h) }},
	{"DELETE", http.MethodDelete,
		func(r *njia.Router, p string, h http.Handler) error { return r.DELETE(p, h) },
		func(g *njia.Group, p string, h http.Handler) error { return g.DELETE(p, h) },
		func(b *njia.Builder, p string, h http.Handler) error { return b.DELETE(p, h) }},
	{"HEAD", http.MethodHead,
		func(r *njia.Router, p string, h http.Handler) error { return r.HEAD(p, h) },
		func(g *njia.Group, p string, h http.Handler) error { return g.HEAD(p, h) },
		func(b *njia.Builder, p string, h http.Handler) error { return b.HEAD(p, h) }},
	{"OPTIONS", http.MethodOptions,
		func(r *njia.Router, p string, h http.Handler) error { return r.OPTIONS(p, h) },
		func(g *njia.Group, p string, h http.Handler) error { return g.OPTIONS(p, h) },
		func(b *njia.Builder, p string, h http.Handler) error { return b.OPTIONS(p, h) }},
	{"ANY", njia.MethodAny,
		func(r *njia.Router, p string, h http.Handler) error { return r.ANY(p, h) },
		func(g *njia.Group, p string, h http.Handler) error { return g.ANY(p, h) },
		func(b *njia.Builder, p string, h http.Handler) error { return b.ANY(p, h) }},
}

// dispatchMethod is a request method the shorthand's route must answer. The
// wildcard has no method of its own, so it is probed with a verb no helper
// names.
func dispatchMethod(method string) string {
	if method == njia.MethodAny {
		return "PROPFIND"
	}
	return method
}

// assertOneRoute checks that exactly one route was registered, under the
// expected method and pattern, and that it actually serves.
func assertOneRoute(t *testing.T, r *njia.Router, method, pattern string) {
	t.Helper()

	routes := r.Routes()
	if len(routes) != 1 {
		t.Fatalf("registered %d routes, want 1", len(routes))
	}
	if routes[0].Method != method {
		t.Errorf("registered under method %q, want %q", routes[0].Method, method)
	}
	if routes[0].PathTemplate != pattern {
		t.Errorf("registered under pattern %q, want %q", routes[0].PathTemplate, pattern)
	}
	if rec := serve(t, r, dispatchMethod(method), pattern); rec.Body.String() != "hit" {
		t.Errorf("%s %s did not reach the handler (body %q)",
			dispatchMethod(method), pattern, rec.Body.String())
	}
}

func TestRouterVerbShorthands(t *testing.T) {
	for _, v := range verbShorthands {
		t.Run(v.name, func(t *testing.T) {
			r := njia.New()
			mustRegister(t, v.router(r, "/x", echo("hit")))
			assertOneRoute(t, r, v.method, "/x")
		})
	}
}

func TestGroupVerbShorthands(t *testing.T) {
	for _, v := range verbShorthands {
		t.Run(v.name, func(t *testing.T) {
			r := njia.New()
			g := r.Group("/api")
			mustRegister(t, v.group(g, "/x", echo("hit")))
			assertOneRoute(t, r, v.method, "/api/x")
		})
	}
}

func TestBuilderVerbShorthands(t *testing.T) {
	for _, v := range verbShorthands {
		t.Run(v.name, func(t *testing.T) {
			r := njia.New()
			if err := r.Swap(func(b *njia.Builder) error {
				return v.build(b, "/x", echo("hit"))
			}); err != nil {
				t.Fatalf("swap: %v", err)
			}
			assertOneRoute(t, r, v.method, "/x")
		})
	}
}

// HandleFunc takes the method as an argument rather than naming it, so one
// method exercises the delegation on each receiver.
func TestHandleFuncOnEveryReceiver(t *testing.T) {
	hit := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hit")) }

	t.Run("Router", func(t *testing.T) {
		r := njia.New()
		mustRegister(t, r.HandleFunc(http.MethodPatch, "/x", hit))
		assertOneRoute(t, r, http.MethodPatch, "/x")
	})

	t.Run("Group", func(t *testing.T) {
		r := njia.New()
		mustRegister(t, r.Group("/api").HandleFunc(http.MethodPatch, "/x", hit))
		assertOneRoute(t, r, http.MethodPatch, "/api/x")
	})

	t.Run("Builder", func(t *testing.T) {
		r := njia.New()
		if err := r.Swap(func(b *njia.Builder) error {
			return b.HandleFunc(http.MethodPatch, "/x", hit)
		}); err != nil {
			t.Fatalf("swap: %v", err)
		}
		assertOneRoute(t, r, http.MethodPatch, "/x")
	})
}

// Builder.Use is the reload path's router-level middleware: a table assembled
// inside Swap has to come out wrapped.
func TestBuilderUseWrapsEveryRoute(t *testing.T) {
	var log []string
	r := njia.New()

	if err := r.Swap(func(b *njia.Builder) error {
		b.Use(tracer("router", &log))
		if err := b.GET("/a", echo("a")); err != nil {
			return err
		}
		return b.GET("/b", echo("b"))
	}); err != nil {
		t.Fatalf("swap: %v", err)
	}

	for _, path := range []string{"/a", "/b"} {
		log = nil
		serve(t, r, http.MethodGet, path)
		if len(log) == 0 {
			t.Errorf("%s: builder middleware did not run", path)
		}
	}
}

// Builder.Mount is the same subtree registration as Router.Mount, reached
// through the reload path.
func TestBuilderMount(t *testing.T) {
	r := njia.New()
	if err := r.Swap(func(b *njia.Builder) error {
		return b.Mount("/admin", echo("mounted"))
	}); err != nil {
		t.Fatalf("swap: %v", err)
	}

	assertBody(t, r, http.MethodGet, "/admin", "mounted")
	assertBody(t, r, "PROPFIND", "/admin/a/b", "mounted "+njia.MountParam+"=a/b")
}
