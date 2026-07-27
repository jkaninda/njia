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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/jkaninda/njia/internal/tree"
)

// The differential harness proves muxcompat matches gorilla. This file proves
// the other half of the Phase 2 contract: that the lookup index changes
// nothing. Every table below is driven through both the indexed path and the
// plain ordered scan, and the two must agree on every observable field.

type tableSpec struct {
	name  string
	build func(*Router)
	paths []string
}

func handler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vars := Vars(r)
		keys := make([]string, 0, len(vars))
		for k := range vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString(name)
		for _, k := range keys {
			fmt.Fprintf(&b, " %s=%s", k, vars[k])
		}
		if rt := CurrentRoute(r); rt != nil {
			fmt.Fprintf(&b, " route=%s", rt.GetName())
		}
		_, _ = io.WriteString(w, b.String())
	})
}

func tableSpecs() []tableSpec {
	return []tableSpec{
		{
			name: "static and wildcard mix",
			build: func(r *Router) {
				r.HandleFunc("/", func(w http.ResponseWriter, rq *http.Request) {}).Name("root")
				r.Handle("/users", handler("users")).Methods("GET").Name("users")
				r.Handle("/users", handler("createUser")).Methods("POST").Name("createUser")
				r.Handle("/users/{id}", handler("user")).Name("user")
				r.Handle("/users/me", handler("me")).Name("me")
				r.Handle("/users/{id}/posts/{post}", handler("post")).Name("post")
				r.Handle("/assets", handler("assets")).Name("assets")
			},
			paths: []string{"/", "/users", "/users/1", "/users/me", "/users/1/posts/2", "/assets", "/nope", "/users/", "/users/1/2"},
		},
		{
			name: "wildcard registered before static",
			build: func(r *Router) {
				r.Handle("/a/{x}", handler("wild")).Name("wild")
				r.Handle("/a/b", handler("static")).Name("static")
				r.Handle("/a/{x}/c", handler("deep")).Name("deep")
				r.Handle("/z", handler("z")).Name("z")
				r.Handle("/y", handler("y")).Name("y")
			},
			paths: []string{"/a/b", "/a/c", "/a/b/c", "/z", "/y", "/a"},
		},
		{
			name: "regex and plain wildcards interleaved",
			build: func(r *Router) {
				r.Handle("/n/{id:[0-9]+}", handler("num")).Name("num")
				r.Handle("/n/{id}", handler("any")).Name("any")
				r.Handle("/n/fixed", handler("fixed")).Name("fixed")
				r.Handle("/m/{a}/{b}", handler("two")).Name("two")
				r.Handle("/k", handler("k")).Name("k")
			},
			paths: []string{"/n/12", "/n/ab", "/n/fixed", "/m/1/2", "/k", "/n"},
		},
		{
			name: "subrouters and prefixes",
			build: func(r *Router) {
				api := r.PathPrefix("/api").Subrouter()
				api.Handle("/users", handler("apiUsers")).Name("apiUsers")
				api.Handle("/users/{id}", handler("apiUser")).Methods("GET").Name("apiUser")
				r.Handle("/api/other", handler("other")).Name("other")
				r.PathPrefix("/static").Handler(handler("static")).Name("staticPrefix")
				r.Handle("/health", handler("health")).Name("health")
				r.Handle("/ready", handler("ready")).Name("ready")
			},
			paths: []string{"/api/users", "/api/users/1", "/api/other", "/staticky", "/static/x", "/health", "/ready", "/api/nope"},
		},
		{
			name: "hosts, queries and headers",
			build: func(r *Router) {
				r.Host("api.example.com").Path("/x").Handler(handler("host")).Name("host")
				r.Handle("/q", handler("q")).Queries("k", "{v}").Name("q")
				r.Handle("/q", handler("qplain")).Name("qplain")
				r.Handle("/h", handler("h")).Headers("X-Test", "yes").Name("h")
				r.Handle("/plain", handler("plain")).Name("plain")
				r.Handle("/plain2", handler("plain2")).Name("plain2")
			},
			paths: []string{"/x", "/q", "/q?k=1", "/h", "/plain", "/plain2"},
		},
		{
			name: "method mismatch across many routes",
			build: func(r *Router) {
				for i := 0; i < 12; i++ {
					r.Handle(fmt.Sprintf("/r%d", i), handler(fmt.Sprintf("r%d", i))).
						Methods("GET").Name(fmt.Sprintf("r%d", i))
				}
				r.Handle("/r3", handler("r3post")).Methods("POST").Name("r3post")
				r.Handle("/w/{id}", handler("w")).Methods("PUT").Name("w")
			},
			paths: []string{"/r0", "/r3", "/r11", "/w/1", "/nope"},
		},
		{
			name: "empty methods and build-only routes",
			build: func(r *Router) {
				r.Handle("/never", handler("never")).Methods().Name("never")
				r.Handle("/never", handler("after")).Name("after")
				r.Handle("/build", handler("build")).BuildOnly().Name("build")
				r.Handle("/ok", handler("ok")).Name("ok")
				r.Handle("/ok2", handler("ok2")).Name("ok2")
			},
			paths: []string{"/never", "/build", "/ok", "/ok2"},
		},
	}
}

func TestIndexMatchesOrderedScan(t *testing.T) {
	methods := []string{"GET", "POST", "PUT", "DELETE"}
	for _, spec := range tableSpecs() {
		spec := spec
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()
			for _, p := range spec.paths {
				for _, m := range methods {
					for _, host := range []string{"", "api.example.com"} {
						indexed := render(t, spec, false, m, p, host)
						scanned := render(t, spec, true, m, p, host)
						if indexed != scanned {
							t.Errorf("index and ordered scan disagree for %s %s host=%q\n  indexed: %s\n  scanned: %s",
								m, p, host, indexed, scanned)
						}
					}
				}
			}
		})
	}
}

// render drives one request through a freshly built router and returns every
// observable outcome as a string.
func render(t *testing.T, spec tableSpec, noIndex bool, method, target, host string) string {
	t.Helper()

	newReq := func() *http.Request {
		req := httptest.NewRequest(method, target, nil)
		if host != "" {
			req.Host = host
		}
		req.Header.Set("X-Test", "yes")
		return req
	}

	r := NewRouter()
	r.noIndex = noIndex
	spec.build(r)

	var match RouteMatch
	matched := r.Match(newReq(), &match)
	name := ""
	if match.Route != nil {
		name = match.Route.GetName()
	}
	keys := make([]string, 0, len(match.Vars))
	for k := range match.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var vars strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&vars, "%s=%s,", k, match.Vars[k])
	}

	r2 := NewRouter()
	r2.noIndex = noIndex
	spec.build(r2)
	rec := httptest.NewRecorder()
	r2.ServeHTTP(rec, newReq())

	return fmt.Sprintf("matched=%v name=%q vars={%s} err=%v status=%d loc=%q body=%q",
		matched, name, vars.String(), match.MatchErr, rec.Code, rec.Header().Get("Location"), rec.Body.String())
}

func TestIndexRebuildsWhenRoutesAreAdded(t *testing.T) {
	r := NewRouter()
	for i := 0; i < 6; i++ {
		r.Handle(fmt.Sprintf("/r%d", i), handler("x")).Name(fmt.Sprintf("r%d", i))
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/late", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	// Registering after the index was built must invalidate it.
	r.Handle("/late", handler("late")).Name("late")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/late", nil))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "late") {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body)
	}
}

func TestIndexFollowsUseEncodedPath(t *testing.T) {
	build := func(r *Router) {
		for i := 0; i < 6; i++ {
			r.Handle(fmt.Sprintf("/r%d/{v}", i), handler("x")).Name(fmt.Sprintf("r%d", i))
		}
	}
	r := NewRouter()
	build(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/r0/a%2Fb", nil))
	decoded := rec.Body.String()

	r2 := NewRouter()
	r2.UseEncodedPath()
	r2.SkipClean(true)
	build(r2)
	rec2 := httptest.NewRecorder()
	r2.ServeHTTP(rec2, httptest.NewRequest("GET", "/r0/a%2Fb", nil))

	if decoded == rec2.Body.String() {
		t.Fatalf("UseEncodedPath made no difference: both %q", decoded)
	}
	if !strings.Contains(rec2.Body.String(), "a%2Fb") {
		t.Fatalf("encoded body = %q, want the escaped form", rec2.Body.String())
	}
}

func TestLazyVarsMaterializeOnDemand(t *testing.T) {
	r := NewRouter()
	for i := 0; i < 6; i++ {
		r.Handle(fmt.Sprintf("/r%d/{a}/{b}", i), handler("x")).Name(fmt.Sprintf("r%d", i))
	}

	var first, second map[string]string
	var route *Route
	r.Handle("/probe/{a}/{b}", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		first = Vars(req)
		second = Vars(req)
		route = CurrentRoute(req)
	})).Name("probe")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/probe/1/2", nil))

	if first == nil || first["a"] != "1" || first["b"] != "2" {
		t.Fatalf("Vars = %v", first)
	}
	// The map must be the same object across calls, because a handler may
	// hold on to it or mutate it the way it could under gorilla.
	first["injected"] = "x"
	if second["injected"] != "x" {
		t.Fatal("Vars returned a different map on the second call")
	}
	if route == nil || route.GetName() != "probe" {
		t.Fatalf("CurrentRoute = %v", route)
	}
}

func TestStaticRouteStillReportsEmptyVars(t *testing.T) {
	r := NewRouter()
	for i := 0; i < 6; i++ {
		r.Handle(fmt.Sprintf("/s%d", i), handler("x")).Name(fmt.Sprintf("s%d", i))
	}
	var vars map[string]string
	var seen bool
	r.Handle("/probe", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		vars = Vars(req)
		seen = true
	})).Name("probe")

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/probe", nil))
	if !seen {
		t.Fatal("handler was not reached")
	}
	if vars == nil {
		t.Fatal("Vars returned nil for a matched static route; gorilla returns an empty map")
	}
	if len(vars) != 0 {
		t.Fatalf("Vars = %v, want empty", vars)
	}
}

func TestVarsOnUnmatchedRequest(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	if Vars(req) != nil {
		t.Fatal("Vars returned a map for a request that matched nothing")
	}
	if CurrentRoute(req) != nil {
		t.Fatal("CurrentRoute returned a route")
	}
	req = SetURLVars(req, map[string]string{"a": "1"})
	if Vars(req)["a"] != "1" {
		t.Fatalf("Vars after SetURLVars = %v", Vars(req))
	}
}

func TestClassificationRejectsWhatItMust(t *testing.T) {
	r := NewRouter()
	fast := r.Handle("/fast/{id}", handler("fast")).Methods("GET")
	slowRegex := r.Handle("/slow/{id:[0-9]+}", handler("slow"))
	slowQuery := r.Handle("/q", handler("q")).Queries("k", "{v}")
	slowHost := r.Host("example.com").Path("/h").Handler(handler("h"))
	slowPrefix := r.PathPrefix("/p").Handler(handler("p"))
	partial := r.Handle("/f/{n}.json", handler("partial"))

	idx := r.buildIndex()
	if idx == nil {
		t.Fatal("no index was built")
	}
	for i, tc := range []struct {
		route *Route
		want  bool
	}{
		{fast, true},
		{slowRegex, false},
		{slowQuery, false},
		{slowHost, false},
		{slowPrefix, false},
		{partial, false},
	} {
		e, ok := classify(tc.route, false)
		if ok != tc.want {
			t.Errorf("route %d: classified fast=%v, want %v", i, ok, tc.want)
		}
		if ok && !e.fast {
			t.Errorf("route %d: entry not marked fast", i)
		}
	}
}

func TestFastSegments(t *testing.T) {
	tests := []struct {
		tpl  string
		ok   bool
		want string
	}{
		{"/a/b", true, "s:a,s:b"},
		{"/a/{x}", true, "s:a,p:x"},
		{"/{x}/{y}", true, "p:x,p:y"},
		{"/a/", true, "s:a,s:"},
		{"/f/{n}.json", false, ""},
		{"/a/{x:[0-9]+}", false, ""},
		{"", true, ""},
		{"a", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.tpl, func(t *testing.T) {
			segs, ok := fastSegments(tc.tpl)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			parts := make([]string, len(segs))
			for i, s := range segs {
				if s.Kind == tree.KindStatic {
					parts[i] = "s:" + s.Text
				} else {
					parts[i] = "p:" + s.Text
				}
			}
			if got := strings.Join(parts, ","); got != tc.want {
				t.Fatalf("segments = %q, want %q", got, tc.want)
			}
		})
	}
}
