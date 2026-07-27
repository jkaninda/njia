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

package difftest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"

	gorilla "github.com/gorilla/mux"
)

// BuildGorilla constructs a gorilla/mux router from the case.
func BuildGorilla(c Case) *gorilla.Router {
	r := gorilla.NewRouter()
	if c.Flags.StrictSlash {
		r.StrictSlash(true)
	}
	if c.Flags.SkipClean {
		r.SkipClean(true)
	}
	if c.Flags.UseEncodedPath {
		r.UseEncodedPath()
	}
	if c.Flags.NotFoundHandler {
		r.NotFoundHandler = markerHandler("custom-404", http.StatusNotFound)
	}
	if c.Flags.MethodNotAllowedHandler {
		r.MethodNotAllowedHandler = markerHandler("custom-405", http.StatusMethodNotAllowed)
	}
	for i := 0; i < c.Flags.Middleware; i++ {
		i := i
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Add("X-Mw", strconv.Itoa(i))
				next.ServeHTTP(w, req)
			})
		})
	}
	for i, s := range c.Routes {
		addGorilla(r, s, strconv.Itoa(i))
	}
	return r
}

func addGorilla(router *gorilla.Router, s RouteSpec, idx string) {
	name := nameOf(s, idx)
	route := router.NewRoute()
	if s.Host != "" {
		route = route.Host(s.Host)
	}
	if s.PathPrefix != "" {
		route = route.PathPrefix(s.PathPrefix)
	}
	if s.Path != "" {
		route = route.Path(s.Path)
	}
	if s.MethodsEmpty {
		route = route.Methods()
	} else if len(s.Methods) > 0 {
		route = route.Methods(copyOf(s.Methods)...)
	}
	if len(s.Queries) > 0 {
		route = route.Queries(copyOf(s.Queries)...)
	}
	if len(s.Headers) > 0 {
		route = route.Headers(copyOf(s.Headers)...)
	}
	if len(s.HeadersRegexp) > 0 {
		route = route.HeadersRegexp(copyOf(s.HeadersRegexp)...)
	}
	if len(s.Schemes) > 0 {
		route = route.Schemes(copyOf(s.Schemes)...)
	}
	if s.Matcher != "" {
		f := Matchers[s.Matcher]
		route = route.MatcherFunc(func(req *http.Request, _ *gorilla.RouteMatch) bool { return f(req) })
	}
	if s.BuildOnly {
		route = route.BuildOnly()
	}
	route = route.Name(name)
	if len(s.Sub) > 0 {
		sub := route.Subrouter()
		for j, ss := range s.Sub {
			addGorilla(sub, ss, idx+"_"+strconv.Itoa(j))
		}
		return
	}
	if !s.NoHandler {
		route.Handler(gorillaHandler(name))
	}
}

func gorillaHandler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := ""
		if rt := gorilla.CurrentRoute(r); rt != nil {
			cur = rt.GetName()
		}
		w.Header().Set("X-Route", cur)
		_, _ = io.WriteString(w, varsBody(name, gorilla.Vars(r)))
	})
}

// RunGorilla executes the case against gorilla/mux.
func RunGorilla(c Case) Result {
	var res Result

	guard(&res, func() {
		r := BuildGorilla(c)
		var match gorilla.RouteMatch
		res.Matched = r.Match(c.request(), &match)
		if match.Route != nil {
			res.MatchedName = match.Route.GetName()
		}
		res.Vars = match.Vars
		if match.MatchErr != nil {
			res.MatchErr = classifyMatchErr(match.MatchErr.Error())
		}

		_ = r.Walk(func(route *gorilla.Route, _ *gorilla.Router, _ []*gorilla.Route) error {
			res.RouteErrs = append(res.RouteErrs, errString(route.GetError()))
			return nil
		})
	})
	if res.Panic != "" {
		return res
	}

	guard(&res, func() {
		r2 := BuildGorilla(c)
		rec := httptest.NewRecorder()
		r2.ServeHTTP(rec, c.request())
		res.StatusCode = rec.Code
		res.Location = rec.Header().Get("Location")
		res.Body = rec.Body.String()
		res.Header = headerDigest(rec.Header())
	})
	return res
}
