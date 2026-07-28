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
