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

// Package bench holds the benchmark grid: gorilla/mux, the standard library
// ServeMux, chi and both njia surfaces, measured at 10, 100 and 1000 routes
// for a static hit, a single-parameter hit, a deeply nested hit, a 404 miss
// and a 405 method mismatch.
package bench

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	chi "github.com/go-chi/chi/v5"
	gorilla "github.com/gorilla/mux"
	"github.com/jkaninda/njia"
	compat "github.com/jkaninda/njia/muxcompat"
)

// sizes is the route-table sizes the grid is measured at.
var sizes = []int{10, 100, 1000}

// nullWriter is a http.ResponseWriter that discards everything, so that the
// benchmark measures routing rather than response recording.
type nullWriter struct{ h http.Header }

func (w *nullWriter) Header() http.Header {
	if w.h == nil {
		w.h = make(http.Header)
	}
	return w.h
}
func (w *nullWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *nullWriter) WriteHeader(int)             {}

func noop(http.ResponseWriter, *http.Request) {}

// routeSet describes the synthetic route table used at each size. The table
// mixes static routes, single-parameter routes and one deep nested route so
// that a single table serves every scenario.
type routeSet struct {
	// staticPaths are fully static templates.
	staticPaths []string
	// paramPaths are templates with one trailing parameter.
	paramPaths []string
	// deepPath is a deeply nested template with a parameter at the end.
	deepPath string
	// Requests exercised by the scenarios.
	staticHit, paramHit, deepHit, miss string
}

func makeRoutes(n int) routeSet {
	rs := routeSet{deepPath: "/deep/a/b/c/d/e/f/g/h/{id}"}
	for i := 0; i < n; i++ {
		rs.staticPaths = append(rs.staticPaths, fmt.Sprintf("/static/%d/resource/leaf", i))
		rs.paramPaths = append(rs.paramPaths, fmt.Sprintf("/param/%d/items/{id}", i))
	}
	last := n - 1
	rs.staticHit = fmt.Sprintf("/static/%d/resource/leaf", last)
	rs.paramHit = fmt.Sprintf("/param/%d/items/4242", last)
	rs.deepHit = "/deep/a/b/c/d/e/f/g/h/4242"
	rs.miss = "/no/such/route/at/all"
	return rs
}

// engine builders

func buildGorilla(rs routeSet) http.Handler {
	r := gorilla.NewRouter()
	for _, p := range rs.staticPaths {
		r.HandleFunc(p, noop).Methods(http.MethodGet)
	}
	for _, p := range rs.paramPaths {
		r.HandleFunc(p, noop).Methods(http.MethodGet)
	}
	r.HandleFunc(rs.deepPath, noop).Methods(http.MethodGet)
	return r
}

func buildCompat(rs routeSet) http.Handler {
	r := compat.NewRouter()
	for _, p := range rs.staticPaths {
		r.HandleFunc(p, noop).Methods(http.MethodGet)
	}
	for _, p := range rs.paramPaths {
		r.HandleFunc(p, noop).Methods(http.MethodGet)
	}
	r.HandleFunc(rs.deepPath, noop).Methods(http.MethodGet)
	return r
}

func buildNative(rs routeSet) http.Handler {
	r := njia.New()
	for _, p := range rs.staticPaths {
		must(r.GET(p, http.HandlerFunc(noop)))
	}
	for _, p := range rs.paramPaths {
		must(r.GET(p, http.HandlerFunc(noop)))
	}
	must(r.GET(rs.deepPath, http.HandlerFunc(noop)))
	return r
}

func buildChi(rs routeSet) http.Handler {
	r := chi.NewRouter()
	for _, p := range rs.staticPaths {
		r.Get(p, noop)
	}
	for _, p := range rs.paramPaths {
		r.Get(chiPath(p), noop)
	}
	r.Get(chiPath(rs.deepPath), noop)
	return r
}

func buildStdlib(rs routeSet) http.Handler {
	m := http.NewServeMux()
	for _, p := range rs.staticPaths {
		m.HandleFunc("GET "+p, noop)
	}
	for _, p := range rs.paramPaths {
		m.HandleFunc("GET "+p, noop)
	}
	m.HandleFunc("GET "+rs.deepPath, noop)
	return m
}

// chiPath converts a gorilla-style template to chi's syntax, which is the
// same for simple {name} parameters.
func chiPath(p string) string { return p }

func must(err error) {
	if err != nil {
		panic(err)
	}
}

var engines = []struct {
	name  string
	build func(routeSet) http.Handler
}{
	{"gorilla", buildGorilla},
	{"stdlib", buildStdlib},
	{"chi", buildChi},
	{"njia-compat", buildCompat},
	{"njia-native", buildNative},
}

func benchServe(b *testing.B, h http.Handler, method, target string) {
	req := httptest.NewRequest(method, target, nil)
	w := &nullWriter{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(w, req)
	}
}

func runGrid(b *testing.B, scenario func(routeSet) (string, string)) {
	for _, size := range sizes {
		rs := makeRoutes(size)
		method, target := scenario(rs)
		for _, e := range engines {
			h := e.build(rs)
			b.Run(fmt.Sprintf("%s/%d", e.name, size), func(b *testing.B) {
				benchServe(b, h, method, target)
			})
		}
	}
}

// BenchmarkRouter_StaticHit measures a request that matches a fully static
// route.
func BenchmarkRouter_StaticHit(b *testing.B) {
	runGrid(b, func(rs routeSet) (string, string) { return http.MethodGet, rs.staticHit })
}

// BenchmarkRouter_ParamHit measures a request that matches a route with one
// path parameter.
func BenchmarkRouter_ParamHit(b *testing.B) {
	runGrid(b, func(rs routeSet) (string, string) { return http.MethodGet, rs.paramHit })
}

// BenchmarkRouter_DeepHit measures a request that matches a deeply nested
// parameterised route.
func BenchmarkRouter_DeepHit(b *testing.B) {
	runGrid(b, func(rs routeSet) (string, string) { return http.MethodGet, rs.deepHit })
}

// BenchmarkRouter_Miss measures a request that matches no route.
func BenchmarkRouter_Miss(b *testing.B) {
	runGrid(b, func(rs routeSet) (string, string) { return http.MethodGet, rs.miss })
}

// BenchmarkRouter_MethodMismatch measures a request whose path matches a route
// but whose method does not.
func BenchmarkRouter_MethodMismatch(b *testing.B) {
	runGrid(b, func(rs routeSet) (string, string) { return http.MethodDelete, rs.staticHit })
}

// BenchmarkRouter_ParamHitWithVars measures a parameter hit where the handler
// actually reads the captured variables, which is where the compatibility tax
// of gorilla's map-based Vars is paid.
func BenchmarkRouter_ParamHitWithVars(b *testing.B) {
	for _, size := range sizes {
		rs := makeRoutes(size)

		b.Run(fmt.Sprintf("gorilla/%d", size), func(b *testing.B) {
			r := gorilla.NewRouter()
			for _, p := range rs.paramPaths {
				r.HandleFunc(p, func(w http.ResponseWriter, req *http.Request) {
					sink = gorilla.Vars(req)["id"]
				}).Methods(http.MethodGet)
			}
			benchServe(b, r, http.MethodGet, rs.paramHit)
		})

		b.Run(fmt.Sprintf("njia-compat/%d", size), func(b *testing.B) {
			r := compat.NewRouter()
			for _, p := range rs.paramPaths {
				r.HandleFunc(p, func(w http.ResponseWriter, req *http.Request) {
					sink = compat.Vars(req)["id"]
				}).Methods(http.MethodGet)
			}
			benchServe(b, r, http.MethodGet, rs.paramHit)
		})

		b.Run(fmt.Sprintf("njia-native/%d", size), func(b *testing.B) {
			r := njia.New()
			for _, p := range rs.paramPaths {
				must(r.GET(p, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					sink = njia.Param(req, "id")
				})))
			}
			benchServe(b, r, http.MethodGet, rs.paramHit)
		})

		b.Run(fmt.Sprintf("chi/%d", size), func(b *testing.B) {
			r := chi.NewRouter()
			for _, p := range rs.paramPaths {
				r.Get(chiPath(p), func(w http.ResponseWriter, req *http.Request) {
					sink = chi.URLParam(req, "id")
				})
			}
			benchServe(b, r, http.MethodGet, rs.paramHit)
		})

		b.Run(fmt.Sprintf("stdlib/%d", size), func(b *testing.B) {
			m := http.NewServeMux()
			for _, p := range rs.paramPaths {
				m.HandleFunc("GET "+p, func(w http.ResponseWriter, req *http.Request) {
					sink = req.PathValue("id")
				})
			}
			benchServe(b, m, http.MethodGet, rs.paramHit)
		})
	}
}

// sink prevents the compiler from optimising away variable lookups.
var sink string

// BenchmarkRegistration measures building a 1000-route table from scratch.
func BenchmarkRegistration(b *testing.B) {
	rs := makeRoutes(1000)
	for _, e := range engines {
		b.Run(e.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = e.build(rs)
			}
		})
	}
}

// BenchmarkLookup measures matching alone, without the request context write
// that ServeHTTP performs, for the two njia surfaces and gorilla.
func BenchmarkLookup(b *testing.B) {
	rs := makeRoutes(1000)

	b.Run("gorilla/static", func(b *testing.B) {
		r := buildGorilla(rs).(*gorilla.Router)
		req := httptest.NewRequest(http.MethodGet, rs.staticHit, nil)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var m gorilla.RouteMatch
			r.Match(req, &m)
		}
	})
	b.Run("njia-compat/static", func(b *testing.B) {
		r := buildCompat(rs).(*compat.Router)
		req := httptest.NewRequest(http.MethodGet, rs.staticHit, nil)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var m compat.RouteMatch
			r.Match(req, &m)
		}
	})
	b.Run("njia-native/static", func(b *testing.B) {
		r := buildNative(rs).(*njia.Router)
		req := httptest.NewRequest(http.MethodGet, rs.staticHit, nil)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r.Lookup(req)
		}
	})
	b.Run("njia-native/param", func(b *testing.B) {
		r := buildNative(rs).(*njia.Router)
		req := httptest.NewRequest(http.MethodGet, rs.paramHit, nil)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r.Lookup(req)
		}
	})
}

// BenchmarkX_Sanity keeps the benchmark grid honest: it asserts that every
// engine actually matches the scenarios, so a benchmark cannot look fast by
// silently 404ing.
func TestGridSanity(t *testing.T) {
	rs := makeRoutes(100)
	for _, e := range engines {
		h := e.build(rs)
		for _, tc := range []struct {
			name, method, target string
			want                 int
		}{
			{"staticHit", http.MethodGet, rs.staticHit, http.StatusOK},
			{"paramHit", http.MethodGet, rs.paramHit, http.StatusOK},
			{"deepHit", http.MethodGet, rs.deepHit, http.StatusOK},
			{"miss", http.MethodGet, rs.miss, http.StatusNotFound},
			{"methodMismatch", http.MethodDelete, rs.staticHit, http.StatusMethodNotAllowed},
		} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))
			if rec.Code != tc.want {
				t.Errorf("%s/%s: got %d, want %d", e.name, tc.name, rec.Code, tc.want)
			}
		}
	}
}

var _ = strconv.Itoa

// host routing

// hostSet is the synthetic virtual-host table: the shape an API gateway builds,
// where many routes share a path and are told apart only by host.
type hostSet struct {
	hosts    []string
	hitHost  string
	hitPath  string
	missHost string
}

func makeHosts(n int) hostSet {
	hs := hostSet{missHost: "absent.example.net"}
	for i := 0; i < n; i++ {
		hs.hosts = append(hs.hosts, fmt.Sprintf("backend%d.example.com", i))
	}
	hs.hitHost = hs.hosts[n-1]
	hs.hitPath = "/api/v1/items/4242"
	return hs
}

func buildGorillaHosts(hs hostSet) http.Handler {
	r := gorilla.NewRouter()
	for _, h := range hs.hosts {
		r.Host(h).Path("/api/v1/items/{id}").Methods(http.MethodGet).HandlerFunc(noop)
	}
	return r
}

func buildNativeHosts(hs hostSet) http.Handler {
	r := njia.New()
	for _, h := range hs.hosts {
		must(r.Host(h).GET("/api/v1/items/{id}", http.HandlerFunc(noop)))
	}
	return r
}

func buildStdlibHosts(hs hostSet) http.Handler {
	m := http.NewServeMux()
	for _, h := range hs.hosts {
		m.HandleFunc("GET "+h+"/api/v1/items/{id}", noop)
	}
	return m
}

var hostEngines = []struct {
	name  string
	build func(hostSet) http.Handler
}{
	{"gorilla", buildGorillaHosts},
	{"stdlib", buildStdlibHosts},
	{"njia-native", buildNativeHosts},
}

func benchHost(b *testing.B, h http.Handler, host, target string) {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = host
	w := &nullWriter{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(w, req)
	}
}

// BenchmarkHost_Hit measures a request that matches an exact virtual host.
func BenchmarkHost_Hit(b *testing.B) {
	for _, size := range sizes {
		hs := makeHosts(size)
		for _, e := range hostEngines {
			h := e.build(hs)
			b.Run(fmt.Sprintf("%s/%d", e.name, size), func(b *testing.B) {
				benchHost(b, h, hs.hitHost, hs.hitPath)
			})
		}
	}
}

// BenchmarkHost_Miss measures a request whose host matches no route, which is
// the case a gateway serves most often when it is being scanned.
func BenchmarkHost_Miss(b *testing.B) {
	for _, size := range sizes {
		hs := makeHosts(size)
		for _, e := range hostEngines {
			h := e.build(hs)
			b.Run(fmt.Sprintf("%s/%d", e.name, size), func(b *testing.B) {
				benchHost(b, h, hs.missHost, hs.hitPath)
			})
		}
	}
}

// BenchmarkHost_Wildcard measures a suffix-wildcard host, which njia resolves
// by scanning an ordered bucket rather than by a map lookup. gorilla expresses
// the same thing with a regular expression.
func BenchmarkHost_Wildcard(b *testing.B) {
	for _, size := range sizes {
		hs := makeHosts(size)

		b.Run(fmt.Sprintf("gorilla/%d", size), func(b *testing.B) {
			r := gorilla.NewRouter()
			for _, h := range hs.hosts {
				r.Host(h).Path("/api/v1/items/{id}").Methods(http.MethodGet).HandlerFunc(noop)
			}
			r.Host("{sub}.tenants.example.com").Path("/api/v1/items/{id}").Methods(http.MethodGet).HandlerFunc(noop)
			benchHost(b, r, "acme.tenants.example.com", hs.hitPath)
		})

		b.Run(fmt.Sprintf("njia-native/%d", size), func(b *testing.B) {
			r := njia.New()
			for _, h := range hs.hosts {
				must(r.Host(h).GET("/api/v1/items/{id}", http.HandlerFunc(noop)))
			}
			must(r.Host("*.tenants.example.com").GET("/api/v1/items/{id}", http.HandlerFunc(noop)))
			benchHost(b, r, "acme.tenants.example.com", hs.hitPath)
		})
	}
}

// TestHostGridSanity keeps the host benchmarks honest.
func TestHostGridSanity(t *testing.T) {
	hs := makeHosts(50)
	for _, e := range hostEngines {
		h := e.build(hs)
		for _, tc := range []struct {
			name, host string
			want       int
		}{
			{"hit", hs.hitHost, http.StatusOK},
			{"miss", hs.missHost, http.StatusNotFound},
		} {
			req := httptest.NewRequest(http.MethodGet, hs.hitPath, nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s/%s: got %d, want %d", e.name, tc.name, rec.Code, tc.want)
			}
		}
	}
}
