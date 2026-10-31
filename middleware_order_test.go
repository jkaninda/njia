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

package njia_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jkaninda/njia"
)

// Use is not positional: where the call sits among the registrations does not
// change what it covers. A route registered before it is wrapped just like one
// registered after.
//
// This is the property that lets a table be assembled in any order, and it is
// what gorilla/mux did — its middleware ran at match time, so it applied to
// every route on the router whatever the registration order. Code moved across
// keeps working.
//
// To wrap only some routes, nest a group or attach the middleware to the route
// with WithMiddleware; the scope is then visible in the structure rather than
// dependent on line order.
func TestUseAppliesRegardlessOfRegistrationOrder(t *testing.T) {
	var authRan []string
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authRan = append(authRan, r.URL.Path)
			next.ServeHTTP(w, r)
		})
	}
	public := echo("public")
	private := echo("private")

	r := njia.New()

	mustRegister(t, r.GET("/public", public))
	r.Use(auth)
	mustRegister(t, r.GET("/private", private))

	serve(t, r, http.MethodGet, "/public")
	serve(t, r, http.MethodGet, "/private")

	ran := func(path string) bool {
		for _, p := range authRan {
			if p == path {
				return true
			}
		}
		return false
	}

	// Registered before the Use, and still covered.
	if !ran("/public") {
		t.Errorf("auth did not run on /public; Use must not depend on registration order")
	}
	// Registered after the Use.
	if !ran("/private") {
		t.Errorf("auth did not run on /private")
	}
}

// The same holds for a group: the middleware covers the whole group whichever
// side of the Use each route was registered on.
func TestGroupUseAppliesRegardlessOfRegistrationOrder(t *testing.T) {
	var log []string
	r := njia.New()
	api := r.Group("/api")

	mustRegister(t, api.GET("/before", echo("before")))
	api.Use(tracer("auth", &log))
	mustRegister(t, api.GET("/after", echo("after")))

	for _, path := range []string{"/api/before", "/api/after"} {
		log = nil
		serve(t, r, http.MethodGet, path)
		if len(log) == 0 {
			t.Errorf("%s: group middleware did not run", path)
		}
	}
}

// Scoping middleware to a subset is done structurally, with a nested group,
// rather than by where Use sits.
func TestScopingMiddlewareWithANestedGroup(t *testing.T) {
	var log []string
	r := njia.New()
	api := r.Group("/api")
	mustRegister(t, api.GET("/public", echo("public")))

	secure := api.Group("/admin", tracer("auth", &log))
	mustRegister(t, secure.GET("/settings", echo("settings")))

	log = nil
	serve(t, r, http.MethodGet, "/api/public")
	if len(log) != 0 {
		t.Errorf("/api/public ran %v, want no auth", log)
	}

	log = nil
	serve(t, r, http.MethodGet, "/api/admin/settings")
	if len(log) == 0 {
		t.Error("/api/admin/settings did not run auth")
	}
}

// --- group middleware scope ---

// tracer returns middleware that records its tag when a request passes through.
func tracer(tag string, log *[]string) njia.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*log = append(*log, tag)
			next.ServeHTTP(w, r)
		})
	}
}

// Middleware added after a route was registered still covers it.
func TestGroupUseAppliesToEarlierRoutes(t *testing.T) {
	var log []string
	r := njia.New()
	g := r.Group("/api")
	mustRegister(t, g.GET("/x", echo("x")))
	g.Use(tracer("auth", &log))

	serve(t, r, http.MethodGet, "/api/x")
	if len(log) == 0 {
		t.Fatal("middleware added after the route did not run")
	}
}

// The case that motivated this: a parent's Use reaches a child group created
// before the call, as well as one created after.
func TestGroupUseReachesChildrenCreatedEitherSide(t *testing.T) {
	var log []string
	r := njia.New()
	api := r.Group("/api")
	v1 := api.Group("/v1") // created before the Use
	api.Use(tracer("auth", &log))
	v2 := api.Group("/v2") // created after the Use

	mustRegister(t, v1.GET("/x", echo("x")))
	mustRegister(t, v2.GET("/y", echo("y")))

	for _, path := range []string{"/api/v1/x", "/api/v2/y"} {
		log = nil
		serve(t, r, http.MethodGet, path)
		if len(log) == 0 {
			t.Errorf("%s: parent middleware did not run", path)
		}
	}
}

// A host group is a child like any other, so an ancestor's Use reaches it.
func TestGroupUseReachesHostGroup(t *testing.T) {
	var log []string
	r := njia.New()
	api := r.Group("/api")
	h := api.Host("example.com")
	mustRegister(t, h.GET("/x", echo("x")))
	api.Use(tracer("auth", &log))

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/api/x", nil)
	serveReq(t, r, req)
	if len(log) == 0 {
		t.Fatal("ancestor middleware did not reach the host group")
	}
}

// Middleware stays scoped: a sibling group is untouched.
func TestGroupMiddlewareDoesNotLeakToSibling(t *testing.T) {
	var log []string
	r := njia.New()
	a := r.Group("/a")
	b := r.Group("/b")
	a.Use(tracer("a-only", &log))
	mustRegister(t, a.GET("/x", echo("ax")))
	mustRegister(t, b.GET("/x", echo("bx")))

	serve(t, r, http.MethodGet, "/b/x")
	if len(log) != 0 {
		t.Fatalf("sibling group ran %v, want nothing", log)
	}
	serve(t, r, http.MethodGet, "/a/x")
	if len(log) == 0 {
		t.Fatal("own group middleware did not run")
	}
}

// Router.Use keeps covering routes registered before it.
func TestRouterUseRemainsRetroactive(t *testing.T) {
	var log []string
	r := njia.New()
	mustRegister(t, r.GET("/x", echo("x")))
	r.Use(tracer("router", &log))

	serve(t, r, http.MethodGet, "/x")
	if len(log) == 0 {
		t.Fatal("Router.Use must apply to routes registered before the call")
	}
}

// Ordering is outermost first: router, then each enclosing group from the
// outside in, then the route's own middleware, then the handler.
func TestMiddlewareOrderIsOutermostFirst(t *testing.T) {
	var log []string
	r := njia.New()
	r.Use(tracer("router", &log))
	outer := r.Group("/api")
	outer.Use(tracer("outer", &log))
	inner := outer.Group("/v1")
	inner.Use(tracer("inner", &log))

	mustRegister(t, inner.GET("/x", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		log = append(log, "handler")
	}), njia.WithMiddleware(tracer("route", &log))))

	serve(t, r, http.MethodGet, "/api/v1/x")

	want := []string{"router", "outer", "inner", "route", "handler"}
	if len(log) != len(want) {
		t.Fatalf("order = %v, want %v", log, want)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Fatalf("order = %v, want %v", log, want)
		}
	}
}

// Use after the table has already been compiled has to rebuild it, or the
// middleware would be invisible to every later request.
func TestGroupUseAfterServingInvalidatesTable(t *testing.T) {
	var log []string
	r := njia.New()
	g := r.Group("/api")
	mustRegister(t, g.GET("/x", echo("x")))

	serve(t, r, http.MethodGet, "/api/x") // compiles the table
	g.Use(tracer("late", &log))
	serve(t, r, http.MethodGet, "/api/x")

	if len(log) == 0 {
		t.Fatal("middleware added after the first request did not take effect")
	}
}

// --- router middleware on unmatched requests ---

// cors answers every OPTIONS request itself and tags every response, the
// shape of a typical CORS middleware.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Router middleware wraps the requests no route serves, so a CORS middleware
// sees a preflight for a GET-only path and an access log sees a 404.
func TestRouterMiddlewareWrapsUnmatched(t *testing.T) {
	r := njia.New()
	r.Use(cors)
	mustRegister(t, r.GET("/api/orders", echo("orders")))

	tests := []struct {
		name, method, target string
		wantCode             int
	}{
		{"preflight is answered by the middleware", http.MethodOptions, "/api/orders", http.StatusNoContent},
		{"not found passes through it", http.MethodGet, "/nope", http.StatusNotFound},
		{"method not allowed passes through it", http.MethodPost, "/api/orders", http.StatusMethodNotAllowed},
		{"a hit still does", http.MethodGet, "/api/orders", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, r, tc.method, tc.target)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Fatal("router middleware did not run")
			}
		})
	}
}

// The 405 keeps its Allow header and the custom handlers still run, inside
// the middleware.
func TestUnmatchedThroughMiddlewareKeepsHandlersAndAllow(t *testing.T) {
	var log []string
	r := njia.New()
	r.Use(tracer("router", &log))
	r.NotFound = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		log = append(log, "notfound")
		w.WriteHeader(http.StatusNotFound)
	})
	r.MethodNotAllowed = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		log = append(log, "405")
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	mustRegister(t, r.GET("/a", echo("a")))

	rec := serve(t, r, http.MethodDelete, "/a")
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
	}
	serve(t, r, http.MethodGet, "/nope")
	if got, want := strings.Join(log, ","), "router,405,router,notfound"; got != want {
		t.Fatalf("ran %q, want %q", got, want)
	}
}

// The clean-path and trailing-slash redirects are answers to unmatched
// requests too.
func TestRedirectsPassThroughRouterMiddleware(t *testing.T) {
	var log []string
	r := njia.New()
	r.CleanPath = true
	r.RedirectTrailingSlash = true
	r.Use(tracer("router", &log))
	mustRegister(t, r.GET("/a/b", echo("ab")))

	for _, target := range []string{"/a//b", "/a/b/"} {
		t.Run(target, func(t *testing.T) {
			log = nil
			rec := serve(t, r, http.MethodGet, target)
			if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/a/b" {
				t.Fatalf("got %d Location=%q, want 301 to /a/b", rec.Code, rec.Header().Get("Location"))
			}
			if len(log) != 1 {
				t.Fatalf("router middleware ran %d times, want 1", len(log))
			}
		})
	}
}

// Group and route middleware wraps matched routes only, and a hit runs router
// middleware exactly once.
func TestOnlyRouterMiddlewareWrapsUnmatched(t *testing.T) {
	var log []string
	r := njia.New()
	r.Use(tracer("router", &log))
	api := r.Group("/api", tracer("group", &log))
	mustRegister(t, api.GET("/x", echo("x"), njia.WithMiddleware(tracer("route", &log))))

	serve(t, r, http.MethodPost, "/api/x")
	serve(t, r, http.MethodGet, "/api/nope")
	if got, want := strings.Join(log, ","), "router,router"; got != want {
		t.Fatalf("misses ran %q, want %q", got, want)
	}

	log = nil
	serve(t, r, http.MethodGet, "/api/x")
	if got, want := strings.Join(log, ","), "router,group,route"; got != want {
		t.Fatalf("hit ran %q, want %q", got, want)
	}
}

// Builder.Use is table-wide middleware and wraps misses the same way, and
// Router.Use still wraps them after a reload.
func TestSwappedTableWrapsUnmatched(t *testing.T) {
	var log []string
	r := njia.New()
	r.Use(tracer("router", &log))
	if err := r.Swap(func(b *njia.Builder) error {
		b.Use(tracer("table", &log))
		return b.GET("/a", echo("a"))
	}); err != nil {
		t.Fatalf("swap: %v", err)
	}

	serve(t, r, http.MethodGet, "/nope")
	if got, want := strings.Join(log, ","), "router,table"; got != want {
		t.Fatalf("ran %q, want %q", got, want)
	}
}
