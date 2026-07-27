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

package difftest_test

import (
	"net/http"
	"testing"

	"github.com/jkaninda/njia/internal/difftest"
)

// TestHarnessIsDeterministic is the Phase 0 sanity self-check: the harness must
// produce identical results when the same engine is run twice.
func TestHarnessIsDeterministic(t *testing.T) {
	for _, c := range behaviorCases() {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			if ok, a, b := difftest.SelfCheck(c); !ok {
				t.Fatalf("gorilla is not deterministic for this case\n  first:  %s\n  second: %s", a, b)
			}
		})
	}
}

// TestBehaviorParity pins every behavior listed in CLAUDE.md section 1.6 by
// observing gorilla and requiring muxcompat to agree.
func TestBehaviorParity(t *testing.T) {
	for _, c := range behaviorCases() {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			difftest.Compare(t, c)
		})
	}
}

func behaviorCases() []difftest.Case {
	static := []difftest.RouteSpec{{Name: "static", Path: "/articles"}}
	param := []difftest.RouteSpec{{Name: "param", Path: "/articles/{id}"}}

	var cases []difftest.Case
	add := func(c difftest.Case) { cases = append(cases, c) }

	// --- basic matching ------------------------------------------------
	add(difftest.Case{Name: "static/hit", Routes: static, Target: "/articles"})
	add(difftest.Case{Name: "static/miss", Routes: static, Target: "/nope"})
	add(difftest.Case{Name: "param/hit", Routes: param, Target: "/articles/42"})
	add(difftest.Case{Name: "param/empty-segment", Routes: param, Target: "/articles/"})
	add(difftest.Case{Name: "param/extra-segment", Routes: param, Target: "/articles/1/2"})
	add(difftest.Case{Name: "param/unicode", Routes: param, Target: "/articles/%E6%97%A5%E6%9C%AC"})
	add(difftest.Case{
		Name:   "regex-param/hit",
		Routes: []difftest.RouteSpec{{Name: "num", Path: "/articles/{id:[0-9]+}"}},
		Target: "/articles/42",
	})
	add(difftest.Case{
		Name:   "regex-param/miss",
		Routes: []difftest.RouteSpec{{Name: "num", Path: "/articles/{id:[0-9]+}"}},
		Target: "/articles/abc",
	})
	add(difftest.Case{
		Name:   "regex-param/nested-braces",
		Routes: []difftest.RouteSpec{{Name: "n", Path: "/x/{id:[0-9]{2,4}}"}},
		Target: "/x/123",
	})
	add(difftest.Case{
		Name:   "multi-param",
		Routes: []difftest.RouteSpec{{Name: "m", Path: "/{a}/{b}/{c}"}},
		Target: "/1/2/3",
	})
	add(difftest.Case{
		Name:   "duplicate-var-name",
		Routes: []difftest.RouteSpec{{Name: "d", Path: "/{id}/{id}"}},
		Target: "/a/b",
	})
	add(difftest.Case{
		Name:   "malformed/unbalanced-brace",
		Routes: []difftest.RouteSpec{{Name: "bad", Path: "/x/{id"}},
		Target: "/x/1",
	})
	add(difftest.Case{
		Name:   "malformed/empty-var",
		Routes: []difftest.RouteSpec{{Name: "bad", Path: "/x/{}"}},
		Target: "/x/1",
	})
	add(difftest.Case{
		Name:   "malformed/empty-pattern",
		Routes: []difftest.RouteSpec{{Name: "bad", Path: "/x/{id:}"}},
		Target: "/x/1",
	})
	add(difftest.Case{
		Name:   "malformed/no-leading-slash",
		Routes: []difftest.RouteSpec{{Name: "bad", Path: "x/{id}"}},
		Target: "/x/1",
	})
	add(difftest.Case{
		Name:   "poisoned-route-is-inert",
		Routes: []difftest.RouteSpec{{Name: "bad", Path: "/x/{id", Methods: []string{"GET"}}, {Name: "good", Path: "/x/1"}},
		Target: "/x/1",
	})

	// --- ordering ------------------------------------------------------
	add(difftest.Case{
		Name: "order/first-registered-wins",
		Routes: []difftest.RouteSpec{
			{Name: "wild", Path: "/a/{x}"},
			{Name: "exact", Path: "/a/b"},
		},
		Target: "/a/b",
	})
	add(difftest.Case{
		Name: "order/exact-first",
		Routes: []difftest.RouteSpec{
			{Name: "exact", Path: "/a/b"},
			{Name: "wild", Path: "/a/{x}"},
		},
		Target: "/a/b",
	})
	add(difftest.Case{
		Name: "order/regex-before-static",
		Routes: []difftest.RouteSpec{
			{Name: "re", Path: "/a/{x:[a-z]+}"},
			{Name: "static", Path: "/a/bb"},
		},
		Target: "/a/bb",
	})

	// --- methods -------------------------------------------------------
	add(difftest.Case{
		Name:   "methods/mismatch-405",
		Routes: []difftest.RouteSpec{{Name: "g", Path: "/a", Methods: []string{"GET"}}},
		Method: http.MethodPost, Target: "/a",
	})
	add(difftest.Case{
		Name:   "methods/mismatch-custom-405",
		Routes: []difftest.RouteSpec{{Name: "g", Path: "/a", Methods: []string{"GET"}}},
		Method: http.MethodPost, Target: "/a",
		Flags: difftest.Flags{MethodNotAllowedHandler: true},
	})
	add(difftest.Case{
		Name:   "methods/empty-list",
		Routes: []difftest.RouteSpec{{Name: "g", Path: "/a", MethodsEmpty: true}},
		Target: "/a",
	})
	add(difftest.Case{
		Name: "methods/later-route-recovers",
		Routes: []difftest.RouteSpec{
			{Name: "get", Path: "/a", Methods: []string{"GET"}},
			{Name: "post", Path: "/a", Methods: []string{"POST"}},
		},
		Method: http.MethodPost, Target: "/a",
	})
	add(difftest.Case{
		Name: "methods/lowercase-normalised",
		Routes: []difftest.RouteSpec{
			{Name: "get", Path: "/a", Methods: []string{"get"}},
		},
		Method: http.MethodGet, Target: "/a",
	})
	add(difftest.Case{
		Name:   "methods/head-vs-get",
		Routes: []difftest.RouteSpec{{Name: "get", Path: "/a", Methods: []string{"GET"}}},
		Method: http.MethodHead, Target: "/a",
	})

	// --- not found -----------------------------------------------------
	add(difftest.Case{
		Name:   "notfound/custom-handler",
		Routes: static, Target: "/nope",
		Flags: difftest.Flags{NotFoundHandler: true},
	})
	add(difftest.Case{
		Name:   "notfound/middleware-wraps-notfound",
		Routes: static, Target: "/nope",
		Flags: difftest.Flags{NotFoundHandler: true, Middleware: 2},
	})
	add(difftest.Case{
		Name:   "notfound/middleware-wraps-405",
		Routes: []difftest.RouteSpec{{Name: "g", Path: "/a", Methods: []string{"GET"}}},
		Method: http.MethodPost, Target: "/a",
		Flags: difftest.Flags{MethodNotAllowedHandler: true, Middleware: 2},
	})
	add(difftest.Case{
		Name:   "middleware/order-on-hit",
		Routes: static, Target: "/articles",
		Flags: difftest.Flags{Middleware: 3},
	})

	// --- path cleaning -------------------------------------------------
	for _, target := range []string{
		"/articles//x", "/articles/../articles", "/articles/./x", "//articles",
		"/articles/x/..", "/articles/", "/",
	} {
		add(difftest.Case{
			Name:   "clean/" + target,
			Routes: []difftest.RouteSpec{{Name: "a", Path: "/articles"}, {Name: "b", Path: "/articles/x"}, {Name: "root", Path: "/"}},
			Target: target,
		})
		add(difftest.Case{
			Name:   "skipclean/" + target,
			Routes: []difftest.RouteSpec{{Name: "a", Path: "/articles"}, {Name: "b", Path: "/articles/x"}, {Name: "root", Path: "/"}},
			Target: target,
			Flags:  difftest.Flags{SkipClean: true},
		})
	}
	add(difftest.Case{
		Name:   "clean/keeps-query",
		Routes: static,
		Target: "/articles//?a=1&b=2",
	})

	// --- strict slash --------------------------------------------------
	for _, tpl := range []string{"/a", "/a/"} {
		for _, target := range []string{"/a", "/a/"} {
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
				add(difftest.Case{
					Name:   "strictslash/tpl=" + tpl + "/req=" + target + "/" + method,
					Routes: []difftest.RouteSpec{{Name: "s", Path: tpl}},
					Method: method, Target: target,
					Flags: difftest.Flags{StrictSlash: true},
				})
				add(difftest.Case{
					Name:   "nostrictslash/tpl=" + tpl + "/req=" + target + "/" + method,
					Routes: []difftest.RouteSpec{{Name: "s", Path: tpl}},
					Method: method, Target: target,
				})
			}
		}
	}
	add(difftest.Case{
		Name:   "strictslash/with-vars",
		Routes: []difftest.RouteSpec{{Name: "s", Path: "/a/{id}/"}},
		Target: "/a/7",
		Flags:  difftest.Flags{StrictSlash: true},
	})
	add(difftest.Case{
		Name:   "strictslash/keeps-query",
		Routes: []difftest.RouteSpec{{Name: "s", Path: "/a/"}},
		Target: "/a?x=1",
		Flags:  difftest.Flags{StrictSlash: true},
	})
	add(difftest.Case{
		Name:   "strictslash/prefix",
		Routes: []difftest.RouteSpec{{Name: "s", PathPrefix: "/a/"}},
		Target: "/a",
		Flags:  difftest.Flags{StrictSlash: true},
	})

	// --- encoded paths -------------------------------------------------
	for _, target := range []string{
		"/enc/a%2Fb", "/enc/a%2fb", "/enc/a+b", "/enc/a%20b", "/enc/%2e%2e", "/enc/caf%C3%A9",
	} {
		add(difftest.Case{
			Name:   "encoded/off/" + target,
			Routes: []difftest.RouteSpec{{Name: "e", Path: "/enc/{v}"}},
			Target: target,
		})
		add(difftest.Case{
			Name:   "encoded/on/" + target,
			Routes: []difftest.RouteSpec{{Name: "e", Path: "/enc/{v}"}},
			Target: target,
			Flags:  difftest.Flags{UseEncodedPath: true},
		})
		add(difftest.Case{
			Name:   "encoded/on+skipclean/" + target,
			Routes: []difftest.RouteSpec{{Name: "e", Path: "/enc/{v}"}},
			Target: target,
			Flags:  difftest.Flags{UseEncodedPath: true, SkipClean: true},
		})
	}

	// --- queries -------------------------------------------------------
	qRoutes := []difftest.RouteSpec{{Name: "q", Path: "/q", Queries: []string{"k", "{v}"}}}
	for _, target := range []string{"/q?k=1", "/q?k=", "/q", "/q?j=1&k=2", "/q?k=1&k=2", "/q?k=a%20b"} {
		add(difftest.Case{Name: "queries/var/" + target, Routes: qRoutes, Target: target})
	}
	qEmpty := []difftest.RouteSpec{{Name: "q", Path: "/q", Queries: []string{"k", ""}}}
	for _, target := range []string{"/q?k=1", "/q?k=", "/q", "/q?j=1"} {
		add(difftest.Case{Name: "queries/empty/" + target, Routes: qEmpty, Target: target})
	}
	qStatic := []difftest.RouteSpec{{Name: "q", Path: "/q", Queries: []string{"k", "v"}}}
	for _, target := range []string{"/q?k=v", "/q?k=w", "/q?k=v&k=w", "/q?k=w&k=v"} {
		add(difftest.Case{Name: "queries/static/" + target, Routes: qStatic, Target: target})
	}
	add(difftest.Case{
		Name:   "queries/multiple-pairs",
		Routes: []difftest.RouteSpec{{Name: "q", Path: "/q", Queries: []string{"a", "{x}", "b", "{y}"}}},
		Target: "/q?a=1&b=2",
	})
	add(difftest.Case{
		Name:   "queries/odd-pairs",
		Routes: []difftest.RouteSpec{{Name: "q", Path: "/q", Queries: []string{"a"}}},
		Target: "/q?a=1",
	})
	add(difftest.Case{
		Name:   "queries/regex",
		Routes: []difftest.RouteSpec{{Name: "q", Path: "/q", Queries: []string{"k", "{v:[0-9]+}"}}},
		Target: "/q?k=abc",
	})

	// --- headers -------------------------------------------------------
	hRoutes := []difftest.RouteSpec{{Name: "h", Path: "/h", Headers: []string{"X-Test", "yes"}}}
	add(difftest.Case{Name: "headers/hit", Routes: hRoutes, Target: "/h", Header: map[string][]string{"X-Test": {"yes"}}})
	add(difftest.Case{Name: "headers/miss", Routes: hRoutes, Target: "/h", Header: map[string][]string{"X-Test": {"no"}}})
	add(difftest.Case{Name: "headers/absent", Routes: hRoutes, Target: "/h"})
	add(difftest.Case{
		Name:   "headers/presence-only",
		Routes: []difftest.RouteSpec{{Name: "h", Path: "/h", Headers: []string{"X-Test", ""}}},
		Target: "/h", Header: map[string][]string{"X-Test": {"anything"}},
	})
	add(difftest.Case{
		Name:   "headers/regexp",
		Routes: []difftest.RouteSpec{{Name: "h", Path: "/h", HeadersRegexp: []string{"X-Test", "^ye"}}},
		Target: "/h", Header: map[string][]string{"X-Test": {"yes"}},
	})
	add(difftest.Case{
		Name:   "headers/multi-value",
		Routes: hRoutes, Target: "/h",
		Header: map[string][]string{"X-Test": {"no", "yes"}},
	})

	// --- schemes -------------------------------------------------------
	add(difftest.Case{
		Name:   "schemes/http",
		Routes: []difftest.RouteSpec{{Name: "s", Path: "/s", Schemes: []string{"https"}}},
		Target: "/s",
	})
	add(difftest.Case{
		Name:   "schemes/tls",
		Routes: []difftest.RouteSpec{{Name: "s", Path: "/s", Schemes: []string{"https"}}},
		Target: "/s", TLS: true,
	})
	add(difftest.Case{
		Name:   "schemes/absolute-url",
		Routes: []difftest.RouteSpec{{Name: "s", Path: "/s", Schemes: []string{"https"}}},
		Target: "https://example.com/s",
	})

	// --- hosts ---------------------------------------------------------
	add(difftest.Case{
		Name:   "host/static-hit",
		Routes: []difftest.RouteSpec{{Name: "h", Host: "example.com", Path: "/x"}},
		Target: "/x", Host: "example.com",
	})
	add(difftest.Case{
		Name:   "host/static-miss",
		Routes: []difftest.RouteSpec{{Name: "h", Host: "example.com", Path: "/x"}},
		Target: "/x", Host: "other.com",
	})
	add(difftest.Case{
		Name:   "host/with-port",
		Routes: []difftest.RouteSpec{{Name: "h", Host: "example.com", Path: "/x"}},
		Target: "/x", Host: "example.com:8080",
	})
	add(difftest.Case{
		Name:   "host/wildcard",
		Routes: []difftest.RouteSpec{{Name: "h", Host: "{sub}.example.com", Path: "/x"}},
		Target: "/x", Host: "api.example.com",
	})
	add(difftest.Case{
		Name:   "host/wildcard-multi-label",
		Routes: []difftest.RouteSpec{{Name: "h", Host: "{sub}.example.com", Path: "/x"}},
		Target: "/x", Host: "a.b.example.com",
	})
	add(difftest.Case{
		Name:   "host/regex",
		Routes: []difftest.RouteSpec{{Name: "h", Host: "{sub:[a-z]+}.example.com", Path: "/x"}},
		Target: "/x", Host: "api.example.com",
	})
	add(difftest.Case{
		Name:   "host/var-collides-with-path-var",
		Routes: []difftest.RouteSpec{{Name: "h", Host: "{v}.example.com", Path: "/{v}"}},
		Target: "/x", Host: "api.example.com",
	})

	// --- prefixes and subrouters ---------------------------------------
	add(difftest.Case{
		Name:   "prefix/hit",
		Routes: []difftest.RouteSpec{{Name: "p", PathPrefix: "/api"}},
		Target: "/api/anything/deep",
	})
	add(difftest.Case{
		Name:   "prefix/exact",
		Routes: []difftest.RouteSpec{{Name: "p", PathPrefix: "/api"}},
		Target: "/api",
	})
	add(difftest.Case{
		Name:   "prefix/partial-segment",
		Routes: []difftest.RouteSpec{{Name: "p", PathPrefix: "/api"}},
		Target: "/apiary",
	})
	add(difftest.Case{
		Name:   "prefix/with-var",
		Routes: []difftest.RouteSpec{{Name: "p", PathPrefix: "/api/{v}"}},
		Target: "/api/1/2",
	})

	subCase := func(name, target string, flags difftest.Flags) difftest.Case {
		return difftest.Case{
			Name: name, Target: target, Flags: flags,
			Routes: []difftest.RouteSpec{{
				Name: "api", PathPrefix: "/api",
				Sub: []difftest.RouteSpec{
					{Name: "users", Path: "/users"},
					{Name: "user", Path: "/users/{id}"},
				},
			}},
		}
	}
	add(subCase("subrouter/hit", "/api/users", difftest.Flags{}))
	add(subCase("subrouter/param", "/api/users/7", difftest.Flags{}))
	add(subCase("subrouter/miss-inside", "/api/other", difftest.Flags{}))
	add(subCase("subrouter/miss-outside", "/other", difftest.Flags{}))
	add(subCase("subrouter/notfound-handler", "/api/other", difftest.Flags{NotFoundHandler: true}))
	add(subCase("subrouter/strictslash", "/api/users/", difftest.Flags{StrictSlash: true}))

	add(difftest.Case{
		Name:   "subrouter/inherits-host",
		Target: "/x", Host: "api.example.com",
		Routes: []difftest.RouteSpec{{
			Name: "hostroute", Host: "{sub}.example.com",
			Sub: []difftest.RouteSpec{{Name: "inner", Path: "/x"}},
		}},
	})
	add(difftest.Case{
		Name:   "subrouter/inherits-method",
		Method: http.MethodPost, Target: "/api/x",
		Routes: []difftest.RouteSpec{{
			Name: "api", PathPrefix: "/api", Methods: []string{"GET"},
			Sub: []difftest.RouteSpec{{Name: "inner", Path: "/x"}},
		}},
	})
	add(difftest.Case{
		Name:   "subrouter/sibling-after-subrouter-miss",
		Target: "/api/other",
		Routes: []difftest.RouteSpec{
			{Name: "api", PathPrefix: "/api", Sub: []difftest.RouteSpec{{Name: "users", Path: "/users"}}},
			{Name: "catchall", PathPrefix: "/"},
		},
	})
	add(difftest.Case{
		Name:   "subrouter/method-mismatch-inside",
		Method: http.MethodPost, Target: "/api/users",
		Routes: []difftest.RouteSpec{
			{Name: "api", PathPrefix: "/api", Sub: []difftest.RouteSpec{{Name: "users", Path: "/users", Methods: []string{"GET"}}}},
		},
	})
	add(difftest.Case{
		Name:   "subrouter/nested-twice",
		Target: "/a/b/c",
		Routes: []difftest.RouteSpec{{
			Name: "a", PathPrefix: "/a",
			Sub: []difftest.RouteSpec{{
				Name: "b", PathPrefix: "/b",
				Sub: []difftest.RouteSpec{{Name: "c", Path: "/c"}},
			}},
		}},
	})
	add(difftest.Case{
		Name:   "subrouter/trailing-slash-prefix",
		Target: "/api/",
		Routes: []difftest.RouteSpec{{
			Name: "api", PathPrefix: "/api/",
			Sub: []difftest.RouteSpec{{Name: "root", Path: ""}, {Name: "users", Path: "/users"}},
		}},
	})

	// --- custom matchers and build-only --------------------------------
	add(difftest.Case{
		Name:   "matcherfunc/true",
		Routes: []difftest.RouteSpec{{Name: "m", Path: "/m", Matcher: "always"}},
		Target: "/m",
	})
	add(difftest.Case{
		Name:   "matcherfunc/false",
		Routes: []difftest.RouteSpec{{Name: "m", Path: "/m", Matcher: "never"}, {Name: "n", Path: "/m"}},
		Target: "/m",
	})
	add(difftest.Case{
		Name:   "matcherfunc/query",
		Routes: []difftest.RouteSpec{{Name: "m", Path: "/m", Matcher: "hasQueryX"}},
		Target: "/m?x=1",
	})
	add(difftest.Case{
		Name:   "buildonly",
		Routes: []difftest.RouteSpec{{Name: "b", Path: "/b", BuildOnly: true}},
		Target: "/b",
	})
	add(difftest.Case{
		Name:   "no-handler",
		Routes: []difftest.RouteSpec{{Name: "n", Path: "/n", NoHandler: true}},
		Target: "/n",
	})
	add(difftest.Case{
		Name:   "no-handler-then-handler",
		Routes: []difftest.RouteSpec{{Name: "n", Path: "/n", NoHandler: true}, {Name: "h", Path: "/n"}},
		Target: "/n",
	})

	// --- long and unusual paths ----------------------------------------
	add(difftest.Case{
		Name:   "deep-nesting",
		Routes: []difftest.RouteSpec{{Name: "d", Path: "/a/b/c/d/e/f/g/h/{i}"}},
		Target: "/a/b/c/d/e/f/g/h/9",
	})
	add(difftest.Case{
		Name:   "root-route",
		Routes: []difftest.RouteSpec{{Name: "root", Path: "/"}},
		Target: "/",
	})
	add(difftest.Case{
		Name:   "empty-path-template",
		Routes: []difftest.RouteSpec{{Name: "e", Path: ""}},
		Target: "/",
	})

	return cases
}
