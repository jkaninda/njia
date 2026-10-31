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

	"github.com/jkaninda/njia"
)

// get serves a GET request for target and returns the status and body.
func get(h http.Handler, target string) string {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return fmt.Sprintf("%d %s", rec.Code, rec.Body.String())
}

func Example() {
	r := njia.New()
	r.GET("/users/{id}", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintf(w, "user %s", njia.Param(req, "id"))
	}))
	r.GET("/files/{rest...}", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprintf(w, "file %s", njia.Param(req, "rest"))
	}))
	if err := r.Err(); err != nil {
		panic(err)
	}

	fmt.Println(get(r, "/users/42"))
	fmt.Println(get(r, "/files/css/app.css"))
	fmt.Println(get(r, "/nope")[:3])
	// Output:
	// 200 user 42
	// 200 file css/app.css
	// 404
}

// Middleware runs outermost first: the router's, then each enclosing group's,
// then the route's own.
func ExampleRouter_Group() {
	tag := func(name string) njia.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				fmt.Fprint(w, name, " > ")
				next.ServeHTTP(w, req)
			})
		}
	}

	r := njia.New()
	r.Use(tag("router"))
	api := r.Group("/api/v1", tag("api"))
	api.GET("/orders", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "orders")
	}), njia.WithMiddleware(tag("route")))

	fmt.Println(get(r, "/api/v1/orders"))
	// Output:
	// 200 router > api > route > orders
}

// Swap validates a whole table before installing it, so a bad reload leaves
// the running routes untouched.
func ExampleRouter_Swap() {
	text := func(s string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, s) })
	}

	r := njia.New()
	_ = r.Swap(func(b *njia.Builder) error {
		return b.GET("/version", text("v1"))
	})

	err := r.Swap(func(b *njia.Builder) error {
		_ = b.GET("/version", text("v2"))
		return b.GET("/broken/{id", text("x"))
	})
	fmt.Println("reload failed:", errors.Is(err, njia.ErrBadPattern))
	fmt.Println(get(r, "/version"))
	// Output:
	// reload failed: true
	// 200 v1
}

// Routes reports the table as registered, which is enough to generate
// documentation such as an OpenAPI specification.
func ExampleRouter_Routes() {
	r := njia.New()
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	r.GET("/users/{id}", noop, njia.WithName("getUser"), njia.WithMeta("summary", "Fetch a user"))
	r.POST("/users", noop, njia.WithName("createUser"))

	for _, ri := range r.Routes() {
		fmt.Println(ri.Method, ri.PathTemplate, ri.Name, ri.Meta["summary"], len(ri.Params))
	}
	// Output:
	// GET /users/{id} getUser Fetch a user 1
	// POST /users createUser <nil> 0
}

// Mount hands a prefix and everything below it to one handler, for every
// method. A more specific route still wins.
func ExampleRouter_Mount() {
	r := njia.New()
	r.Mount("/static", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, "static ", req.URL.Path)
	}))
	r.GET("/static/health", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "healthy")
	}))

	fmt.Println(get(r, "/static/css/app.css"))
	fmt.Println(get(r, "/static/health"))
	// Output:
	// 200 static /static/css/app.css
	// 200 healthy
}

// Host restricts a group to host patterns; a placeholder captures a label.
func ExampleRouter_Host() {
	r := njia.New()
	tenants := r.Host("{tenant}.example.com")
	tenants.GET("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, "tenant ", njia.Param(req, "tenant"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "acme.example.com"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	fmt.Println(rec.Body.String())
	// Output:
	// tenant acme
}

// Every registration error is a *RouteError naming the route, wrapping one of
// the package's sentinels.
func ExampleRouteError() {
	r := njia.New()
	err := r.GET("/users/{id:int}", http.NotFoundHandler())

	var re *njia.RouteError
	if errors.As(err, &re) {
		fmt.Println(re.Method, re.Pattern)
	}
	fmt.Println(errors.Is(err, njia.ErrBadPattern))
	// Output:
	// GET /users/{id:int}
	// true
}

// A Builder collects every error, so a gateway can report all the bad
// entries in a configuration file rather than only the first.
func ExampleBuilder_Errs() {
	b := njia.NewBuilder()
	h := http.NotFoundHandler()
	_ = b.GET("/ok", h)
	_ = b.GET("no-slash", h)
	_ = b.GET("/ok", h)

	for _, err := range b.Errs() {
		fmt.Println(err)
	}
	// Output:
	// njia: route GET "no-slash": route pattern must start with a slash
	// njia: route GET "/ok": duplicate route: GET /ok already registered
}

// The zero Router is ready to use.
func ExampleRouter_zero() {
	var r njia.Router
	r.GET("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "home") }))
	fmt.Println(get(&r, "/"))
	// Output:
	// 200 home
}
