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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jkaninda/njia"
)

// serveHost drives a request with an explicit Host header.
func serveHost(t *testing.T, r *njia.Router, method, host, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestHostPatternForms(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		accept  []string
		reject  []string
	}{
		{
			name:    "exact",
			pattern: "api.example.com",
			accept:  []string{"api.example.com", "API.EXAMPLE.COM", "api.example.com:8080", "api.example.com."},
			reject:  []string{"example.com", "a.api.example.com", "api.example.org", "xapi.example.com"},
		},
		{
			name:    "exact with port",
			pattern: "api.example.com:8443",
			accept:  []string{"api.example.com:8443", "API.example.com:8443"},
			reject:  []string{"api.example.com", "api.example.com:8080"},
		},
		{
			name:    "suffix wildcard spans labels",
			pattern: "*.example.com",
			accept:  []string{"api.example.com", "a.b.example.com", "API.Example.Com", "x.example.com:9000"},
			reject:  []string{"example.com", "example.com.evil.net", ".example.com", "notexample.com"},
		},
		{
			name:    "single label placeholder",
			pattern: "{sub}.example.com",
			accept:  []string{"api.example.com", "x.example.com:1"},
			reject:  []string{"example.com", "a.b.example.com"},
		},
		{
			name:    "catch-all placeholder with suffix",
			pattern: "{sub...}.example.com",
			accept:  []string{"api.example.com", "a.b.c.example.com"},
			reject:  []string{"example.com"},
		},
		{
			name:    "whole host placeholder",
			pattern: "{h...}",
			accept:  []string{"anything", "a.b.c", "example.com:8080"},
			reject:  nil,
		},
		{
			name:    "any",
			pattern: "*",
			accept:  []string{"anything.at.all", "example.com:1234"},
			reject:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := njia.New()
			mustRegister(t, r.Host(tc.pattern).GET("/x", echo("hit")))

			for _, h := range tc.accept {
				if code := serveHost(t, r, "GET", h, "/x").Code; code != http.StatusOK {
					t.Errorf("host %q: status %d, want 200", h, code)
				}
			}
			for _, h := range tc.reject {
				if code := serveHost(t, r, "GET", h, "/x").Code; code != http.StatusNotFound {
					t.Errorf("host %q: status %d, want 404", h, code)
				}
			}
		})
	}
}

func TestHostPatternErrors(t *testing.T) {
	bad := []string{
		"", "example.com/path", "exam ple.com", "api.*", "api.*.com",
		"{sub}", "{}.example.com", "{a}{b}.example.com", "{sub}x.example.com",
		"*.", "*..com", ".example.com", "a..b.com", "example.com:",
		"::1:8080", "[::1", "[::1]x",
	}
	for _, p := range bad {
		p := p
		t.Run(p, func(t *testing.T) {
			if err := njia.ValidateHost(p); !errors.Is(err, njia.ErrBadHost) {
				t.Fatalf("ValidateHost(%q) = %v, want ErrBadHost", p, err)
			}
			r := njia.New()
			err := r.Host(p).GET("/x", echo("x"))
			if !errors.Is(err, njia.ErrBadHost) {
				t.Fatalf("registration err = %v, want ErrBadHost", err)
			}
			var re *njia.RouteError
			if !errors.As(err, &re) {
				t.Fatalf("err = %v, want it to carry route identity", err)
			}
		})
	}

	good := []string{
		"example.com", "example.com:8443", "*.example.com", "{s}.example.com",
		"{s...}.example.com", "{h...}", "*", "[::1]", "[::1]:8080", "EXAMPLE.COM",
	}
	for _, p := range good {
		if err := njia.ValidateHost(p); err != nil {
			t.Errorf("ValidateHost(%q) = %v, want nil", p, err)
		}
	}
}

func TestHostSpecificityOrder(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.Host("*").GET("/x", echo("any")))
	mustRegister(t, r.Host("*.example.com").GET("/x", echo("suffix")))
	mustRegister(t, r.Host("{sub}.example.com").GET("/x", echo("label")))
	mustRegister(t, r.Host("api.example.com").GET("/x", echo("exact")))
	mustRegister(t, r.Host("api.example.com:8443").GET("/x", echo("exact-port")))

	tests := []struct{ host, want string }{
		{"api.example.com:8443", "exact-port"},
		{"api.example.com", "exact"},
		{"api.example.com:80", "exact"},
		{"www.example.com", "label sub=www"},
		{"a.b.example.com", "suffix"},
		{"other.org", "any"},
	}
	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			rec := serveHost(t, r, "GET", tc.host, "/x")
			if rec.Code != 200 {
				t.Fatalf("status = %d", rec.Code)
			}
			if rec.Body.String() != tc.want {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.want)
			}
		})
	}
}

// TestPathSpecificityBeatsHostSpecificity pins the precedence decision: a more
// specific path wins even when another route has a more specific host. It is
// what keeps a gateway's global /healthz reachable underneath per-host
// catch-all proxy routes.
func TestPathSpecificityBeatsHostSpecificity(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/healthz", echo("global-health")))
	mustRegister(t, r.Host("api.example.com").GET("/{rest...}", echo("proxy")))

	if got := serveHost(t, r, "GET", "api.example.com", "/healthz").Body.String(); got != "global-health" {
		t.Fatalf("body = %q, want the global health route to win", got)
	}
	if got := serveHost(t, r, "GET", "api.example.com", "/orders/1").Body.String(); got != "proxy rest=orders/1" {
		t.Fatalf("body = %q", got)
	}
	// The proxy route is host-scoped, so another host falls through to 404.
	if code := serveHost(t, r, "GET", "other.com", "/orders/1").Code; code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
}

func TestHostFallsBackToLessSpecificForMethod(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.Host("api.example.com").GET("/x", echo("host-get")))
	mustRegister(t, r.POST("/x", echo("any-post")))

	if got := serveHost(t, r, "GET", "api.example.com", "/x").Body.String(); got != "host-get" {
		t.Fatalf("body = %q", got)
	}
	// The exact-host variant has no POST, so the host-agnostic one serves it.
	if got := serveHost(t, r, "POST", "api.example.com", "/x").Body.String(); got != "any-post" {
		t.Fatalf("body = %q", got)
	}
}

func TestHostMissIs404AndMethodMissIs405(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.Host("api.example.com").GET("/x", echo("hit")))

	if code := serveHost(t, r, "GET", "other.com", "/x").Code; code != http.StatusNotFound {
		t.Fatalf("wrong host: status = %d, want 404", code)
	}
	rec := serveHost(t, r, "DELETE", "api.example.com", "/x")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method: status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q", got)
	}
	// The Allow header must not leak methods that only another host serves.
	mustRegister(t, r.Host("other.com").PUT("/x", echo("other")))
	if got := serveHost(t, r, "DELETE", "api.example.com", "/x").Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q, want it scoped to the request's host", got)
	}
}

func TestHostParameters(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.Host("{tenant}.app.example.com").GET("/users/{id}", echo("tenant")))

	rec := serveHost(t, r, "GET", "acme.app.example.com", "/users/7")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	// The host parameter is reported before every path parameter.
	if got := rec.Body.String(); got != "tenant tenant=acme id=7" {
		t.Fatalf("body = %q", got)
	}

	var (
		tenant string
		m      map[string]string
		info   []njia.ParamInfo
	)
	mustRegister(t, r.Host("{tenant}.app.example.com").GET("/probe/{id}",
		http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			tenant = njia.Param(req, "tenant")
			m = njia.ParamMap(req)
			info = njia.RouteOf(req).Params()
		})))
	serveHost(t, r, "GET", "beta.app.example.com", "/probe/9")

	if tenant != "beta" {
		t.Fatalf("Param(tenant) = %q", tenant)
	}
	if m["tenant"] != "beta" || m["id"] != "9" {
		t.Fatalf("ParamMap = %v", m)
	}
	if len(info) != 2 || !info[0].InHost || info[0].Name != "tenant" || info[0].Position != -1 {
		t.Fatalf("Params = %+v", info)
	}
	if info[1].InHost || info[1].Name != "id" {
		t.Fatalf("Params = %+v", info)
	}
}

func TestMultipleHostsOnOneRoute(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/x", echo("multi"), njia.WithHost("a.example.com", "b.example.com")))

	for _, h := range []string{"a.example.com", "b.example.com"} {
		if code := serveHost(t, r, "GET", h, "/x").Code; code != 200 {
			t.Errorf("host %q: status %d", h, code)
		}
	}
	if code := serveHost(t, r, "GET", "c.example.com", "/x").Code; code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}

	routes := r.Routes()
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want 1", len(routes))
	}
	if strings.Join(routes[0].Hosts, ",") != "a.example.com,b.example.com" {
		t.Fatalf("Hosts = %v", routes[0].Hosts)
	}
}

func TestWithHostOverridesGroup(t *testing.T) {
	r := njia.New()
	g := r.Host("group.example.com")
	mustRegister(t, g.GET("/inherited", echo("inherited")))
	mustRegister(t, g.GET("/overridden", echo("overridden"), njia.WithHost("other.example.com")))

	if code := serveHost(t, r, "GET", "group.example.com", "/inherited").Code; code != 200 {
		t.Errorf("inherited: status %d", code)
	}
	if code := serveHost(t, r, "GET", "group.example.com", "/overridden").Code; code != http.StatusNotFound {
		t.Errorf("overridden should not answer on the group host: status %d", code)
	}
	if code := serveHost(t, r, "GET", "other.example.com", "/overridden").Code; code != 200 {
		t.Errorf("overridden: status %d", code)
	}
}

func TestHostGroupsCompose(t *testing.T) {
	r := njia.New()
	var order []string
	mark := func(name string) njia.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, req)
			})
		}
	}

	host := r.Host("api.example.com")
	host.Use(mark("host-mw"))
	v1 := host.Group("/api/v1", mark("group-mw"))
	mustRegister(t, v1.GET("/orders/{id}", echo("order")))

	if got := v1.Hosts(); len(got) != 1 || got[0] != "api.example.com" {
		t.Fatalf("nested group Hosts = %v, want it inherited", got)
	}
	rec := serveHost(t, r, "GET", "api.example.com", "/api/v1/orders/3")
	if rec.Code != 200 || rec.Body.String() != "order id=3" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body)
	}
	if strings.Join(order, ",") != "host-mw,group-mw" {
		t.Fatalf("middleware order = %v", order)
	}
	if code := serveHost(t, r, "GET", "other.com", "/api/v1/orders/3").Code; code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
}

func TestHostRegistrationErrors(t *testing.T) {
	tests := []struct {
		name string
		do   func(*njia.Router) error
		want error
	}{
		{"duplicate under the same host", func(r *njia.Router) error {
			_ = r.Host("a.com").GET("/x", echo("a"))
			return r.Host("a.com").GET("/x", echo("b"))
		}, njia.ErrDuplicateRoute},
		{"duplicate host-agnostic", func(r *njia.Router) error {
			_ = r.GET("/x", echo("a"))
			return r.GET("/x", echo("b"))
		}, njia.ErrDuplicateRoute},
		{"star duplicates host-agnostic", func(r *njia.Router) error {
			_ = r.GET("/x", echo("a"))
			return r.Host("*").GET("/x", echo("b"))
		}, njia.ErrDuplicateRoute},
		{"duplicate among several hosts", func(r *njia.Router) error {
			_ = r.Host("a.com").GET("/x", echo("a"))
			return r.GET("/x", echo("b"), njia.WithHost("b.com", "a.com"))
		}, njia.ErrDuplicateRoute},
		{"host captures differ", func(r *njia.Router) error {
			return r.GET("/x", echo("a"), njia.WithHost("{a}.x.com", "{b}.y.com"))
		}, njia.ErrParamConflict},
		{"some hosts capture and some do not", func(r *njia.Router) error {
			return r.GET("/x", echo("a"), njia.WithHost("{a}.x.com", "*.y.com"))
		}, njia.ErrParamConflict},
		{"host and path capture the same name", func(r *njia.Router) error {
			return r.Host("{id}.x.com").GET("/u/{id}", echo("a"))
		}, njia.ErrParamConflict},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := njia.New()
			if err := tc.do(r); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDifferentHostsShareAPathWithoutConflict(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.Host("a.com").GET("/x", echo("a")))
	mustRegister(t, r.Host("b.com").GET("/x", echo("b")))
	mustRegister(t, r.Host("a.com").POST("/x", echo("a-post")))
	mustRegister(t, r.GET("/x", echo("fallback")))

	for _, tc := range []struct{ method, host, want string }{
		{"GET", "a.com", "a"},
		{"GET", "b.com", "b"},
		{"POST", "a.com", "a-post"},
		{"GET", "c.com", "fallback"},
	} {
		if got := serveHost(t, r, tc.method, tc.host, "/x").Body.String(); got != tc.want {
			t.Errorf("%s %s: body = %q, want %q", tc.method, tc.host, got, tc.want)
		}
	}

	// POST is registered on a.com only, so it is a method mismatch elsewhere
	// rather than silently falling through to another host's handler.
	if code := serveHost(t, r, "POST", "b.com", "/x").Code; code != http.StatusMethodNotAllowed {
		t.Errorf("POST b.com: status = %d, want 405", code)
	}
	if got := serveHost(t, r, "POST", "b.com", "/x").Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want the methods b.com actually serves", got)
	}
}

// TestGomaShape exercises the route table shape Goma Gateway actually builds:
// many routes that share a catch-all path and are told apart only by host,
// alongside global endpoints that must stay reachable on every host.
func TestGomaShape(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/healthz", echo("healthz")))
	mustRegister(t, r.GET("/readyz", echo("readyz")))
	mustRegister(t, r.GET("/metrics", echo("metrics")))

	backends := []string{
		"okapi.example.com", "wordpress.example.com", "grafana.example.com",
		"api.example.com", "marketing.example.com", "staging.example.com",
		"admin.internal", "shop.example.com",
	}
	for _, h := range backends {
		mustRegister(t, r.Host(h).GET("/{rest...}", echo("proxy:"+h)))
		mustRegister(t, r.Host(h).POST("/{rest...}", echo("proxy-post:"+h)))
	}
	// A wildcard tenant catch-all underneath the named backends.
	mustRegister(t, r.Host("*.tenants.example.com").GET("/{rest...}", echo("tenant")))

	for _, tc := range []struct{ host, path, want string }{
		{"okapi.example.com", "/anything/deep", "proxy:okapi.example.com rest=anything/deep"},
		{"grafana.example.com", "/d/abc", "proxy:grafana.example.com rest=d/abc"},
		{"admin.internal", "/", "proxy:admin.internal rest="},
		{"a.tenants.example.com", "/x", "tenant rest=x"},
		{"a.b.tenants.example.com", "/x", "tenant rest=x"},
		{"okapi.example.com", "/healthz", "healthz"},
		{"grafana.example.com", "/metrics", "metrics"},
	} {
		t.Run(tc.host+tc.path, func(t *testing.T) {
			rec := serveHost(t, r, "GET", tc.host, tc.path)
			if rec.Code != 200 {
				t.Fatalf("status = %d", rec.Code)
			}
			if rec.Body.String() != tc.want {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.want)
			}
		})
	}

	// An unknown host has no proxy route, so only the global endpoints answer.
	if code := serveHost(t, r, "GET", "unknown.example.com", "/anything").Code; code != http.StatusNotFound {
		t.Fatalf("unknown host: status = %d, want 404", code)
	}
	if code := serveHost(t, r, "GET", "unknown.example.com", "/healthz").Code; code != 200 {
		t.Fatalf("global endpoint on unknown host: status = %d, want 200", code)
	}
	if code := serveHost(t, r, "DELETE", "okapi.example.com", "/x").Code; code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", code)
	}
}

func TestHostSwapUnderLoad(t *testing.T) {
	r := njia.New()
	build := func(gen int) func(*njia.Builder) error {
		return func(b *njia.Builder) error {
			if err := b.GET("/healthz", echo("healthz")); err != nil {
				return err
			}
			for i := 0; i < 20; i++ {
				h := fmt.Sprintf("b%d.example.com", i)
				if err := b.Host(h).GET("/{rest...}", echo(fmt.Sprintf("v%d:%s", gen, h))); err != nil {
					return err
				}
			}
			return nil
		}
	}
	if err := r.Swap(build(0)); err != nil {
		t.Fatal(err)
	}
	if got := serveHost(t, r, "GET", "b7.example.com", "/a").Body.String(); !strings.HasSuffix(got, "b7.example.com rest=a") {
		t.Fatalf("body = %q", got)
	}
	for gen := 1; gen < 20; gen++ {
		if err := r.Swap(build(gen)); err != nil {
			t.Fatalf("swap %d: %v", gen, err)
		}
	}
	if got := serveHost(t, r, "GET", "b3.example.com", "/z").Body.String(); got != "v19:b3.example.com rest=z" {
		t.Fatalf("body = %q", got)
	}
	if got := serveHost(t, r, "GET", "b3.example.com", "/healthz").Body.String(); got != "healthz" {
		t.Fatalf("body = %q", got)
	}
}

// TestHostFreeTableIgnoresTheHost proves the zero-cost claim: when no route
// constrains the host, the request's host is never consulted, so even a
// nonsensical one routes normally.
func TestHostFreeTableIgnoresTheHost(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.GET("/x", echo("x")))
	mustRegister(t, r.GET("/y/{id}", echo("y")))

	for _, h := range []string{"", "not a host", "::::", "[unclosed"} {
		req := httptest.NewRequest("GET", "/x", nil)
		req.Host = h
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("host %q: status %d, want 200", h, rec.Code)
		}
	}
}

func TestHostLookupDoesNotAllocate(t *testing.T) {
	r := njia.New()
	for i := 0; i < 200; i++ {
		h := fmt.Sprintf("b%d.example.com", i)
		mustRegister(t, r.Host(h).GET("/api/{id}", echo("x")))
		mustRegister(t, r.Host(h).GET("/static/leaf", echo("x")))
	}
	mustRegister(t, r.Host("*.wild.example.com").GET("/api/{id}", echo("w")))
	mustRegister(t, r.Host("{t}.tenant.example.com").GET("/api/{id}", echo("t")))

	cases := []struct{ name, host, path string }{
		{"exact host, param path", "b199.example.com", "/api/7"},
		{"exact host, static path", "b199.example.com", "/static/leaf"},
		{"suffix wildcard host", "a.b.wild.example.com", "/api/7"},
		{"placeholder host", "acme.tenant.example.com", "/api/7"},
		{"unmatched host", "nope.example.com", "/api/7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			req.Host = tc.host
			buf := make([]njia.PathParam, 0, 8)
			if n := testing.AllocsPerRun(200, func() {
				_, out, _ := r.LookupInto(req, buf)
				buf = out[:0]
			}); n != 0 {
				t.Errorf("LookupInto allocated %v times per run, want 0", n)
			}
		})
	}
}

func TestHostIsCaseInsensitiveIncludingUppercasePatterns(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.Host("API.Example.COM").GET("/x", echo("hit")))
	for _, h := range []string{"api.example.com", "API.EXAMPLE.COM", "Api.Example.Com"} {
		if code := serveHost(t, r, "GET", h, "/x").Code; code != 200 {
			t.Errorf("host %q: status %d", h, code)
		}
	}
}

func TestHostFromAbsoluteRequestURI(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.Host("api.example.com").GET("/x", echo("hit")))

	// A proxy-style absolute request URI carries the authority in the URL.
	req := httptest.NewRequest("GET", "http://api.example.com/x", nil)
	req.Host = "something.else"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
