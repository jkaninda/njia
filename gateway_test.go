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

// Tests for the features a reverse proxy needs: wildcard methods and explicit
// priority.

package njia_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/jkaninda/njia"
)

func assertBody(t *testing.T, r *njia.Router, method, target, want string) {
	t.Helper()
	rec := serve(t, r, method, target)
	if rec.Body.String() != want {
		t.Fatalf("%s %s = %q, want %q", method, target, rec.Body.String(), want)
	}
}

// A gateway forwards whatever verb the client sent, so ANY has to accept verbs
// that no helper names and that no RFC defines.
func TestAnyMethodAcceptsArbitraryVerbs(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.ANY("/api/{rest...}", echo("proxy")))

	for _, m := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodHead, http.MethodTrace,
		"PROPFIND", "MKCOL", "REPORT", "LOCK", "X-VENDOR-VERB",
	} {
		rec := serve(t, r, m, "/api/v1/things")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", m, rec.Code)
		}
	}
}

// The wildcard still captures parameters.
func TestAnyMethodCapturesParams(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.ANY("/api/{rest...}", echo("proxy")))
	assertBody(t, r, "PROPFIND", "/api/a/b", "proxy rest=a/b")
}

// An explicitly registered method always beats the wildcard, so a route can
// serve one verb specially and proxy the rest.
func TestAnyMethodExplicitWins(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.ANY("/files/{path...}", echo("any")))
	mustRegister(t, r.GET("/files/{path...}", echo("get")))

	assertBody(t, r, http.MethodGet, "/files/x", "get path=x")
	assertBody(t, r, http.MethodPost, "/files/x", "any path=x")
	assertBody(t, r, "PROPFIND", "/files/x", "any path=x")
}

// HEAD keeps falling back to GET before it reaches the wildcard, matching what
// net/http itself does.
func TestAnyMethodHeadPrefersGet(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.ANY("/x", echo("any")))
	mustRegister(t, r.GET("/x", echo("get")))
	assertBody(t, r, http.MethodHead, "/x", "get")
}

// A path served by a wildcard accepts every method, so it can never answer 405.
func TestAnyMethodNever405(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.ANY("/x", echo("any")))
	for _, m := range []string{http.MethodPost, http.MethodDelete, "WHATEVER"} {
		if rec := serve(t, r, m, "/x"); rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", m, rec.Code)
		}
	}
}

// The wildcard sentinel is a registration detail and must never reach the wire.
func TestAllowHeaderNeverAdvertisesWildcard(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/only-get", echo("g")))
	mustRegister(t, r.ANY("/anything", echo("a")))

	rec := serve(t, r, http.MethodPost, "/only-get")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("Allow = %q, want %q", allow, "GET, HEAD")
	}
}

// Registering the wildcard twice on one pattern is a duplicate, like any other
// method.
func TestAnyMethodDuplicate(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.ANY("/x", echo("a")))
	if err := r.ANY("/x", echo("b")); !errors.Is(err, njia.ErrDuplicateRoute) {
		t.Fatalf("err = %v, want ErrDuplicateRoute", err)
	}
}

// priority

// Without priority the more specific pattern wins, which is the default this
// package has always had.
func TestPriorityDefaultIsSpecificity(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.ANY("/api/{rest...}", echo("catchall")))
	mustRegister(t, r.ANY("/api/v1/{rest...}", echo("specific")))
	assertBody(t, r, http.MethodGet, "/api/v1/x", "specific rest=x")
}

// A lower priority pulls a less specific pattern ahead of a more specific one,
// which is what a gateway needs to shadow a route deliberately.
func TestPriorityOverridesSpecificity(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.ANY("/api/{rest...}", echo("catchall"), njia.WithPriority(-1)))
	mustRegister(t, r.ANY("/api/v1/{rest...}", echo("specific")))
	assertBody(t, r, http.MethodGet, "/api/v1/x", "catchall rest=v1/x")
}

// Priority also has to beat the static fast path, which otherwise answers with
// an exact hit without consulting any other candidate.
func TestPriorityBeatsStaticFastPath(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/api/health", echo("static")))
	mustRegister(t, r.ANY("/api/{rest...}", echo("catchall"), njia.WithPriority(-1)))
	assertBody(t, r, http.MethodGet, "/api/health", "catchall rest=health")
}

// A positive priority pushes a pattern behind the default, so the more specific
// route wins even though it was registered second.
func TestPriorityPushesBehindDefault(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.ANY("/api/{rest...}", echo("catchall"), njia.WithPriority(10)))
	mustRegister(t, r.GET("/api/health", echo("static")))
	assertBody(t, r, http.MethodGet, "/api/health", "static")
	// The catch-all still serves what nothing more specific claims.
	assertBody(t, r, http.MethodGet, "/api/other", "catchall rest=other")
}

// Ordering picks a pattern before it picks a method, so routes sharing a
// pattern share the lowest priority any of them asked for.
func TestPrioritySharedAcrossPatternRoutes(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/api/{rest...}", echo("get"), njia.WithPriority(-5)))
	mustRegister(t, r.POST("/api/{rest...}", echo("post")))
	mustRegister(t, r.ANY("/api/v1/{rest...}", echo("specific")))

	// Both methods on the shared pattern outrank the more specific route.
	assertBody(t, r, http.MethodGet, "/api/v1/x", "get rest=v1/x")
	assertBody(t, r, http.MethodPost, "/api/v1/x", "post rest=v1/x")
}

// Priority is reported by introspection.
func TestPriorityReportedByRoutes(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/a", echo("a"), njia.WithPriority(-3)))
	mustRegister(t, r.GET("/b", echo("b")))

	got := map[string]int{}
	for _, ri := range r.Routes() {
		got[ri.PathTemplate] = ri.Priority
	}
	if got["/a"] != -3 {
		t.Errorf("/a priority = %d, want -3", got["/a"])
	}
	if got["/b"] != njia.DefaultPriority {
		t.Errorf("/b priority = %d, want %d", got["/b"], njia.DefaultPriority)
	}
}

// A table that uses priority must still 404 paths nothing matches, since it
// takes a different lookup path than the default one.
func TestPriorityStillNotFound(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.ANY("/api/{rest...}", echo("catchall"), njia.WithPriority(-1)))
	if rec := serve(t, r, http.MethodGet, "/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// ...and still 405 a path it matches for a method it does not serve.
func TestPriorityStillMethodNotAllowed(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/api/{rest...}", echo("get"), njia.WithPriority(-1)))
	rec := serve(t, r, http.MethodPost, "/api/x")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
