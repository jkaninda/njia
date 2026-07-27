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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jkaninda/njia"
)

// echo returns a handler that writes the given tag followed by every captured
// parameter, so a test can assert on both the route and its parameters.
func echo(tag string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString(tag)
		for i := 0; ; i++ {
			name, value, ok := njia.ParamAt(r, i)
			if !ok {
				break
			}
			fmt.Fprintf(&b, " %s=%s", name, value)
		}
		_, _ = io.WriteString(w, b.String())
	})
}

func serve(t *testing.T, r *njia.Router, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func mustRegister(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}
}

func TestMatching(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/", echo("root")))
	mustRegister(t, r.GET("/users", echo("users")))
	mustRegister(t, r.GET("/users/me", echo("me")))
	mustRegister(t, r.GET("/users/{id}", echo("user")))
	mustRegister(t, r.GET("/users/{id}/posts/{postID}", echo("post")))
	mustRegister(t, r.GET("/files/{rest...}", echo("files")))
	mustRegister(t, r.POST("/users", echo("create")))

	tests := []struct {
		name, method, target string
		wantCode             int
		wantBody             string
	}{
		{"root", "GET", "/", 200, "root"},
		{"static collection", "GET", "/users", 200, "users"},
		{"static beats wildcard", "GET", "/users/me", 200, "me"},
		{"wildcard captures", "GET", "/users/42", 200, "user id=42"},
		{"two wildcards", "GET", "/users/42/posts/7", 200, "post id=42 postID=7"},
		{"catch-all captures rest", "GET", "/files/a/b/c.txt", 200, "files rest=a/b/c.txt"},
		{"catch-all matches bare prefix", "GET", "/files", 200, "files rest="},
		{"catch-all matches empty rest", "GET", "/files/", 200, "files rest="},
		{"method selects handler", "POST", "/users", 200, "create"},
		{"unknown path is 404", "GET", "/nope", 404, ""},
		{"wildcard does not match empty segment", "GET", "/users/", 404, ""},
		{"wildcard does not span slashes", "GET", "/users/1/2", 404, ""},
		{"unknown method is 405", "DELETE", "/users", 405, ""},
		{"HEAD falls back to GET", "HEAD", "/users", 200, "users"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, r, tc.method, tc.target)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.wantCode, rec.Body)
			}
			if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestMethodNotAllowedReportsAllow(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/x", echo("get")))
	mustRegister(t, r.PUT("/x", echo("put")))

	rec := serve(t, r, "DELETE", "/x")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD, PUT" {
		t.Fatalf("Allow = %q, want %q", got, "GET, HEAD, PUT")
	}
}

func TestSpecificityBeatsRegistrationOrder(t *testing.T) {
	// The wildcard is registered first; the static route must still win, which
	// is where the native API deliberately differs from gorilla.
	r := njia.New()
	mustRegister(t, r.GET("/a/{x}", echo("wild")))
	mustRegister(t, r.GET("/a/b", echo("static")))
	mustRegister(t, r.GET("/{y}/{z}", echo("both-wild")))

	if got := serve(t, r, "GET", "/a/b").Body.String(); got != "static" {
		t.Fatalf("body = %q, want %q", got, "static")
	}
	if got := serve(t, r, "GET", "/a/c").Body.String(); got != "wild x=c" {
		t.Fatalf("body = %q, want %q", got, "wild x=c")
	}
	if got := serve(t, r, "GET", "/q/c").Body.String(); got != "both-wild y=q z=c" {
		t.Fatalf("body = %q, want %q", got, "both-wild y=q z=c")
	}
}

func TestRegistrationErrors(t *testing.T) {
	tests := []struct {
		name string
		do   func(*njia.Router) error
		want error
	}{
		{"no leading slash", func(r *njia.Router) error { return r.GET("users", echo("x")) }, njia.ErrNoLeadingSlash},
		{"partial segment", func(r *njia.Router) error { return r.GET("/f/{n}.json", echo("x")) }, njia.ErrBadPattern},
		{"empty name", func(r *njia.Router) error { return r.GET("/f/{}", echo("x")) }, njia.ErrBadPattern},
		{"repeated name", func(r *njia.Router) error { return r.GET("/{a}/{a}", echo("x")) }, njia.ErrBadPattern},
		{"constraint rejected", func(r *njia.Router) error { return r.GET("/{a:nope}", echo("x")) }, njia.ErrBadPattern},
		{"regex constraint rejected", func(r *njia.Router) error { return r.GET("/{a:[0-9]+}", echo("x")) }, njia.ErrBadPattern},
		{"empty constraint rejected", func(r *njia.Router) error { return r.GET("/{a:}", echo("x")) }, njia.ErrBadPattern},
		{"catch-all not last", func(r *njia.Router) error { return r.GET("/{a...}/b", echo("x")) }, njia.ErrCatchAllPosition},
		{"nil handler", func(r *njia.Router) error { return r.GET("/x", nil) }, njia.ErrNoHandler},
		{"empty method", func(r *njia.Router) error { return r.Handle("", "/x", echo("x")) }, njia.ErrEmptyMethod},
		{"duplicate route", func(r *njia.Router) error {
			_ = r.GET("/dup", echo("a"))
			return r.GET("/dup", echo("b"))
		}, njia.ErrDuplicateRoute},
		{"duplicate param route", func(r *njia.Router) error {
			_ = r.GET("/dup/{id}", echo("a"))
			return r.GET("/dup/{id}", echo("b"))
		}, njia.ErrDuplicateRoute},
		{"param name conflict", func(r *njia.Router) error {
			_ = r.GET("/c/{id}", echo("a"))
			return r.GET("/c/{name}", echo("b"))
		}, njia.ErrParamConflict},
		{"duplicate name", func(r *njia.Router) error {
			_ = r.GET("/n1", echo("a"), njia.WithName("dup"))
			return r.GET("/n2", echo("b"), njia.WithName("dup"))
		}, njia.ErrDuplicateName},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := njia.New()
			err := tc.do(r)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			var re *njia.RouteError
			if !errors.As(err, &re) && !errors.Is(err, njia.ErrNoLeadingSlash) {
				t.Fatalf("err = %v, want it to carry route identity", err)
			}
		})
	}
}

func TestRegistrationNeverPanics(t *testing.T) {
	// Goma Gateway builds routes from user-supplied YAML; a malformed entry
	// must never take the process down.
	hostile := []string{
		"", "users", "/{", "/}", "/{}", "/{a", "/a/{b...}/c", "/{a:unknown}",
		"/{a}/{a}", "//", "/{a...}/{b...}", strings.Repeat("/x", 500),
		"/{a:int}", "/ünï/{cödé}", "/%2F/{x}",
	}
	r := njia.New()
	for _, p := range hostile {
		p := p
		t.Run(p, func(t *testing.T) {
			defer func() {
				if v := recover(); v != nil {
					t.Fatalf("registration panicked on %q: %v", p, v)
				}
			}()
			_ = r.GET(p, echo("x"))
		})
	}
	// The router must still work after all those failures.
	mustRegister(t, r.GET("/healthy", echo("ok")))
	if got := serve(t, r, "GET", "/healthy").Body.String(); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

func TestGroupsAndMiddlewareOrder(t *testing.T) {
	var order []string
	mark := func(name string) njia.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	r := njia.New()
	r.Use(mark("router1"), mark("router2"))
	api := r.Group("/api", mark("group1"))
	v1 := api.Group("/v1", mark("group2"))
	mustRegister(t, v1.GET("/users/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler:"+njia.Param(r, "id"))
	}), njia.WithMiddleware(mark("route1"))))

	serve(t, r, "GET", "/api/v1/users/9")

	want := []string{"router1", "router2", "group1", "group2", "route1", "handler:9"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestGroupPrefixJoining(t *testing.T) {
	for _, tc := range []struct{ prefix, pattern, want string }{
		{"/api", "/users", "/api/users"},
		{"/api/", "/users", "/api/users"},
		{"/api", "users", "/api/users"},
		{"/api", "/", "/api"},
		{"/api", "", "/api"},
		{"", "/users", "/users"},
		{"/a", "/b/{c}", "/a/b/{c}"},
	} {
		t.Run(tc.prefix+"+"+tc.pattern, func(t *testing.T) {
			r := njia.New()
			g := r.Group(tc.prefix)
			mustRegister(t, g.GET(tc.pattern, echo("x")))
			if got := r.Routes()[0].PathTemplate; got != tc.want {
				t.Fatalf("template = %q, want %q", got, tc.want)
			}
			if got := g.Prefix(); got != tc.prefix {
				t.Fatalf("Prefix() = %q, want %q", got, tc.prefix)
			}
		})
	}
}

func TestNestedGroupPrefixes(t *testing.T) {
	r := njia.New()
	api := r.Group("/api")
	v1 := api.Group("/v1")
	admin := v1.Group("/admin")
	mustRegister(t, admin.DELETE("/users/{id}", echo("del")))
	if got := r.Routes()[0].PathTemplate; got != "/api/v1/admin/users/{id}" {
		t.Fatalf("template = %q", got)
	}
	if code := serve(t, r, "DELETE", "/api/v1/admin/users/3").Code; code != 200 {
		t.Fatalf("status = %d", code)
	}
}

func TestIntrospection(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/users/{id}", echo("u"),
		njia.WithName("getUser"),
		njia.WithMeta("summary", "Fetch one user"),
		njia.WithMeta("tags", []string{"users"})))
	mustRegister(t, r.POST("/users", echo("c"), njia.WithName("createUser")))
	mustRegister(t, r.GET("/files/{rest...}", echo("f")))

	routes := r.Routes()
	if len(routes) != 3 {
		t.Fatalf("got %d routes, want 3", len(routes))
	}

	first := routes[0]
	if first.Method != "GET" || first.PathTemplate != "/users/{id}" || first.Name != "getUser" {
		t.Fatalf("unexpected route %+v", first)
	}
	if len(first.Params) != 1 {
		t.Fatalf("got %d params, want 1", len(first.Params))
	}
	p := first.Params[0]
	if p.Name != "id" || p.Position != 1 || p.CatchAll {
		t.Fatalf("unexpected param %+v", p)
	}
	if got, _ := first.Meta["summary"].(string); got != "Fetch one user" {
		t.Fatalf("meta summary = %v", first.Meta["summary"])
	}
	if first.Handler == nil {
		t.Fatal("handler not reported")
	}

	if last := routes[2]; !last.Params[0].CatchAll || last.Params[0].Name != "rest" {
		t.Fatalf("unexpected catch-all param %+v", last.Params[0])
	}

	if rt := r.Route("createUser"); rt == nil || rt.Pattern() != "/users" || rt.Method() != "POST" {
		t.Fatalf("named lookup failed: %+v", rt)
	}
	if rt := r.Route("nope"); rt != nil {
		t.Fatalf("unknown name returned %+v", rt)
	}

	// Routes must be a snapshot: mutating it must not affect the router.
	routes[0].Name = "mutated"
	if r.Routes()[0].Name != "getUser" {
		t.Fatal("Routes returned a view into router state")
	}
}

// TestOpenAPIFromRoutesAlone is the Phase 4 acceptance criterion: a full
// specification can be generated from Routes() with no other information.
//
// Parameter value types are deliberately not part of it. Patterns carry a name
// and nothing else, so a generator gets each parameter's name, position and
// whether it is a catch-all, and takes the schema type from WithMeta or from
// its own annotations.
func TestOpenAPIFromRoutesAlone(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/users/{id}", echo("u"),
		njia.WithMeta("summary", "Get user"), njia.WithMeta("schema.id", "integer")))
	mustRegister(t, r.DELETE("/users/{id}", echo("d"), njia.WithMeta("summary", "Delete user")))
	mustRegister(t, r.GET("/orders/{orderID}/items/{index}", echo("o")))

	type operation struct {
		method  string
		summary string
		params  []njia.ParamInfo
	}
	paths := map[string][]operation{}
	for _, ri := range r.Routes() {
		// The template is already the OpenAPI path: nothing has to be stripped
		// out of it, and nothing reconstructed from a regular expression.
		summary, _ := ri.Meta["summary"].(string)
		paths[ri.PathTemplate] = append(paths[ri.PathTemplate],
			operation{ri.Method, summary, ri.Params})
	}

	if len(paths) != 2 {
		t.Fatalf("got %d paths, want 2: %v", len(paths), paths)
	}
	ops := paths["/users/{id}"]
	if len(ops) != 2 || ops[0].method != "GET" || ops[1].method != "DELETE" {
		t.Fatalf("unexpected operations %+v", ops)
	}
	if ops[0].summary != "Get user" {
		t.Fatalf("summary = %q", ops[0].summary)
	}
	if p := ops[0].params[0]; p.Name != "id" || p.Position != 1 || p.CatchAll {
		t.Fatalf("parameter not fully described: %+v", p)
	}
	// A schema type is an annotation now, not something the pattern encodes.
	if got, _ := r.Routes()[0].Meta["schema.id"].(string); got != "integer" {
		t.Fatalf("schema annotation = %q, want %q", got, "integer")
	}

	order := paths["/orders/{orderID}/items/{index}"]
	if len(order) != 1 || len(order[0].params) != 2 {
		t.Fatalf("unexpected order operation %+v", order)
	}
	if a, b := order[0].params[0], order[0].params[1]; a.Name != "orderID" || a.Position != 1 ||
		b.Name != "index" || b.Position != 3 {
		t.Fatalf("unexpected order parameters %+v %+v", a, b)
	}
}

func TestParamAccessors(t *testing.T) {
	r := njia.New()
	var (
		gotMap   map[string]string
		gotCount int
		gotRoute *njia.Route
		appended []njia.PathParam
	)
	mustRegister(t, r.GET("/a/{x}/b/{y}", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotMap = njia.ParamMap(req)
		gotCount = njia.NumParams(req)
		gotRoute = njia.RouteOf(req)
		appended = njia.AppendParams(req, appended)
	})))

	serve(t, r, "GET", "/a/1/b/2")

	if gotCount != 2 {
		t.Fatalf("NumParams = %d, want 2", gotCount)
	}
	if gotMap["x"] != "1" || gotMap["y"] != "2" {
		t.Fatalf("ParamMap = %v", gotMap)
	}
	if gotRoute == nil || gotRoute.Pattern() != "/a/{x}/b/{y}" {
		t.Fatalf("RouteOf = %+v", gotRoute)
	}
	if len(appended) != 2 || appended[0].Name != "x" || appended[1].Value != "2" {
		t.Fatalf("AppendParams = %+v", appended)
	}
}

func TestParamOnUnmatchedRequest(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	if got := njia.Param(req, "id"); got != "" {
		t.Fatalf("Param = %q, want empty", got)
	}
	if _, _, ok := njia.ParamAt(req, 0); ok {
		t.Fatal("ParamAt reported a parameter")
	}
	if njia.RouteOf(req) != nil {
		t.Fatal("RouteOf reported a route")
	}
	if njia.ParamMap(req) != nil {
		t.Fatal("ParamMap reported parameters")
	}

	req = njia.SetParams(req, njia.PathParam{Name: "id", Value: "7"})
	if got := njia.Param(req, "id"); got != "7" {
		t.Fatalf("Param after SetParams = %q, want 7", got)
	}
}

func TestRouteInContextForStaticRoutes(t *testing.T) {
	r := njia.New()
	var seen *njia.Route
	mustRegister(t, r.GET("/static", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen = njia.RouteOf(req)
	})))

	serve(t, r, "GET", "/static")
	if seen != nil {
		t.Fatal("a static route wrote to the context with RouteInContext off")
	}

	r.RouteInContext = true
	serve(t, r, "GET", "/static")
	if seen == nil || seen.Pattern() != "/static" {
		t.Fatalf("RouteOf = %+v, want the static route", seen)
	}
}

func TestManyParamsSpillToHeap(t *testing.T) {
	r := njia.New()
	var pattern strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&pattern, "/{p%d}", i)
	}
	mustRegister(t, r.GET(pattern.String(), echo("many")))

	var target strings.Builder
	var want strings.Builder
	want.WriteString("many")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&target, "/v%d", i)
		fmt.Fprintf(&want, " p%d=v%d", i, i)
	}
	if got := serve(t, r, "GET", target.String()).Body.String(); got != want.String() {
		t.Fatalf("body = %q, want %q", got, want.String())
	}
}

func TestCleanPathAndTrailingSlash(t *testing.T) {
	r := njia.New()
	r.CleanPath = true
	r.RedirectTrailingSlash = true
	mustRegister(t, r.GET("/a/b", echo("ab")))

	rec := serve(t, r, "GET", "/a//b")
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/a/b" {
		t.Fatalf("clean: status %d location %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = serve(t, r, "GET", "/a/b/")
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/a/b" {
		t.Fatalf("trailing slash: status %d location %q", rec.Code, rec.Header().Get("Location"))
	}

	plain := njia.New()
	mustRegister(t, plain.GET("/a/b", echo("ab")))
	if code := serve(t, plain, "GET", "/a/b/").Code; code != http.StatusNotFound {
		t.Fatalf("without RedirectTrailingSlash, status = %d, want 404", code)
	}
}

func TestCustomNotFoundAndMethodNotAllowed(t *testing.T) {
	r := njia.New()
	r.NotFound = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "nope")
	})
	r.MethodNotAllowed = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusExpectationFailed)
	})
	mustRegister(t, r.GET("/x", echo("x")))

	if rec := serve(t, r, "GET", "/y"); rec.Code != http.StatusTeapot || rec.Body.String() != "nope" {
		t.Fatalf("not found: %d %q", rec.Code, rec.Body)
	}
	rec := serve(t, r, "POST", "/x")
	if rec.Code != http.StatusExpectationFailed {
		t.Fatalf("method not allowed: %d", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q even with a custom handler", got)
	}
}

func TestSwapValidatesBeforeInstalling(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/v1", echo("v1")))

	err := r.Swap(func(b *njia.Builder) error {
		if err := b.GET("/v2", echo("v2")); err != nil {
			return err
		}
		return b.GET("/{bad", echo("bad"))
	})
	if !errors.Is(err, njia.ErrBadPattern) {
		t.Fatalf("err = %v, want ErrBadPattern", err)
	}
	if got := serve(t, r, "GET", "/v1").Body.String(); got != "v1" {
		t.Fatalf("the old table was disturbed: %q", got)
	}
	if code := serve(t, r, "GET", "/v2").Code; code != 404 {
		t.Fatalf("a route from the rejected table was installed: %d", code)
	}

	// A build function that reports an error itself is also rejected.
	sentinel := errors.New("config invalid")
	if err := r.Swap(func(b *njia.Builder) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the caller's error", err)
	}
	if got := serve(t, r, "GET", "/v1").Body.String(); got != "v1" {
		t.Fatalf("the old table was disturbed: %q", got)
	}

	// A valid build replaces the table wholesale.
	if err := r.Swap(func(b *njia.Builder) error { return b.GET("/v2", echo("v2")) }); err != nil {
		t.Fatal(err)
	}
	if code := serve(t, r, "GET", "/v1").Code; code != 404 {
		t.Fatalf("the old table survived the swap: %d", code)
	}
	if got := serve(t, r, "GET", "/v2").Body.String(); got != "v2" {
		t.Fatalf("body = %q", got)
	}
}

// TestSwapUnderLoad is the Goma Gateway acceptance criterion: reloading a
// route table while requests are in flight must drop nothing and panic never,
// including when some of the candidate tables are invalid.
func TestSwapUnderLoad(t *testing.T) {
	r := njia.New()
	if err := r.Swap(func(b *njia.Builder) error { return b.GET("/api/{id}", echo("v0")) }); err != nil {
		t.Fatal(err)
	}

	var (
		served  atomic.Int64
		dropped atomic.Int64
		stop    atomic.Bool
		wg      sync.WaitGroup
	)

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/7", nil))
				if rec.Code != http.StatusOK || !strings.HasSuffix(rec.Body.String(), "id=7") {
					dropped.Add(1)
				}
				served.Add(1)
			}
		}()
	}

	for gen := 0; gen < 300; gen++ {
		gen := gen
		if gen%3 == 0 {
			// Every third reload is malformed and must be rejected without
			// disturbing the table that is serving.
			if err := r.Swap(func(b *njia.Builder) error {
				if err := b.GET("/api/{id}", echo(fmt.Sprintf("v%d", gen))); err != nil {
					return err
				}
				return b.GET("/broken/{a...}/b", echo("x"))
			}); err == nil {
				t.Error("a malformed table was accepted")
			}
			continue
		}
		if err := r.Swap(func(b *njia.Builder) error {
			return b.GET("/api/{id}", echo(fmt.Sprintf("v%d", gen)))
		}); err != nil {
			t.Errorf("valid swap %d failed: %v", gen, err)
		}
	}

	// Let the request goroutines run until they have covered enough of the
	// reload window for the assertion below to mean something, so the test
	// does not silently pass on a loaded machine.
	for i := 0; i < 10000 && served.Load() < 2000; i++ {
		runtime.Gosched()
	}
	stop.Store(true)
	wg.Wait()

	if served.Load() < 100 {
		t.Fatalf("only %d requests were served; the test proved little", served.Load())
	}
	if d := dropped.Load(); d != 0 {
		t.Fatalf("%d of %d requests were dropped during reload", d, served.Load())
	}
}

func TestLookupDoesNotAllocate(t *testing.T) {
	r := njia.New()
	for i := 0; i < 200; i++ {
		mustRegister(t, r.GET(fmt.Sprintf("/static/%d/leaf", i), echo("s")))
		mustRegister(t, r.GET(fmt.Sprintf("/param/%d/{id}", i), echo("p")))
	}
	staticReq := httptest.NewRequest("GET", "/static/199/leaf", nil)
	paramReq := httptest.NewRequest("GET", "/param/199/abc", nil)

	if n := testing.AllocsPerRun(200, func() { r.Lookup(staticReq) }); n != 0 {
		t.Errorf("static Lookup allocated %v times per run, want 0", n)
	}
	if n := testing.AllocsPerRun(200, func() { r.Lookup(paramReq) }); n != 0 {
		t.Errorf("param Lookup allocated %v times per run, want 0", n)
	}

	buf := make([]njia.PathParam, 0, 8)
	if n := testing.AllocsPerRun(200, func() {
		_, out, _ := r.LookupInto(paramReq, buf)
		buf = out[:0]
	}); n != 0 {
		t.Errorf("LookupInto allocated %v times per run, want 0", n)
	}
}

// nullWriter discards a response so that allocation measurements reflect
// routing rather than response recording.
type nullWriter struct{ h http.Header }

func (w *nullWriter) Header() http.Header {
	if w.h == nil {
		w.h = make(http.Header)
	}
	return w.h
}
func (w *nullWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *nullWriter) WriteHeader(int)             {}

func TestStaticServeHTTPDoesNotAllocate(t *testing.T) {
	r := njia.New()
	for i := 0; i < 200; i++ {
		mustRegister(t, r.GET(fmt.Sprintf("/static/%d/leaf", i), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	}
	req := httptest.NewRequest("GET", "/static/199/leaf", nil)
	w := &nullWriter{}
	_ = w.Header()

	if n := testing.AllocsPerRun(200, func() { r.ServeHTTP(w, req) }); n != 0 {
		t.Errorf("static ServeHTTP allocated %v times per run, want 0", n)
	}
}

func TestRoutesReflectSwap(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/before", echo("b")))
	if len(r.Routes()) != 1 {
		t.Fatal("route not reported")
	}
	if err := r.Swap(func(b *njia.Builder) error {
		if err := b.GET("/after1", echo("a1")); err != nil {
			return err
		}
		return b.GET("/after2", echo("a2"))
	}); err != nil {
		t.Fatal(err)
	}
	got := r.Routes()
	if len(got) != 2 || got[0].PathTemplate != "/after1" {
		t.Fatalf("Routes after swap = %+v", got)
	}
	if !strings.Contains(r.String(), "/after2") {
		t.Fatalf("String() = %q", r.String())
	}
}

func TestBuilderCollectsErrors(t *testing.T) {
	b := njia.NewBuilder()
	_ = b.GET("/ok", echo("ok"))
	_ = b.GET("bad", echo("bad"))
	_ = b.GET("/{also bad", echo("bad"))
	if len(b.Errs()) != 2 {
		t.Fatalf("got %d errors, want 2: %v", len(b.Errs()), b.Errs())
	}
	if b.Err() == nil {
		t.Fatal("Err returned nil")
	}
}

// TestCatchAllDoesNotShadowShorterRoutes pins the position-by-position
// specificity rule: a pattern that has run out of segments is more specific
// than one whose catch-all would swallow the rest of the path.
func TestCatchAllDoesNotShadowShorterRoutes(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/api/{rest...}", echo("catchall")))
	mustRegister(t, r.GET("/api", echo("exact")))
	mustRegister(t, r.GET("/api/v1", echo("v1")))
	mustRegister(t, r.GET("/api/{id}", echo("param")))
	mustRegister(t, r.GET("/{root...}", echo("root")))
	mustRegister(t, r.GET("/", echo("slash")))

	for _, tc := range []struct{ path, want string }{
		{"/api", "exact"},
		{"/api/v1", "v1"},
		{"/api/9", "param id=9"},
		{"/api/a/b", "catchall rest=a/b"},
		{"/", "slash"},
		{"/other", "root root=other"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := serve(t, r, "GET", tc.path)
			if rec.Code != 200 {
				t.Fatalf("status = %d", rec.Code)
			}
			if rec.Body.String() != tc.want {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.want)
			}
		})
	}
}

// TestSlowPathAgreesWithFastPath drives the same table through the direct
// lookup and through the fallback scan that a method mismatch forces, and
// requires them to select the same route.
func TestSlowPathAgreesWithFastPath(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/api/{rest...}", echo("catchall")))
	mustRegister(t, r.POST("/api/{rest...}", echo("catchall-post")))
	mustRegister(t, r.GET("/api", echo("exact")))
	mustRegister(t, r.GET("/api/v1", echo("v1")))

	// POST /api is not registered on the exact route, so the router falls back
	// to the catch-all; the fallback must not change which path pattern wins
	// for the methods that are registered.
	if got := serve(t, r, "GET", "/api").Body.String(); got != "exact" {
		t.Fatalf("GET /api = %q", got)
	}
	if got := serve(t, r, "POST", "/api").Body.String(); got != "catchall-post rest=" {
		t.Fatalf("POST /api = %q", got)
	}
	if got := serve(t, r, "POST", "/api/v1").Body.String(); got != "catchall-post rest=v1" {
		t.Fatalf("POST /api/v1 = %q", got)
	}
	if code := serve(t, r, "DELETE", "/api/v1").Code; code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /api/v1 status = %d, want 405", code)
	}
}
