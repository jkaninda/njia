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
	"net/http"
	"strings"
)

// CORSMethodMiddleware returns middleware that sets the
// Access-Control-Allow-Methods response header to the methods accepted by the
// routes matching the request path, provided one of them accepts OPTIONS.
func CORSMethodMiddleware(r *Router) MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			allMethods, err := getAllMethodsForRoute(r, req)
			if err == nil {
				for _, v := range allMethods {
					if v == http.MethodOptions {
						w.Header().Set("Access-Control-Allow-Methods", strings.Join(allMethods, ","))
					}
				}
			}
			next.ServeHTTP(w, req)
		})
	}
}

// getAllMethodsForRoute returns the union of the methods accepted by every
// route whose other matchers accept the request.
func getAllMethodsForRoute(r *Router, req *http.Request) ([]string, error) {
	var allMethods []string
	for _, route := range r.routes {
		var match RouteMatch
		if route.Match(req, &match) || match.MatchErr == ErrMethodMismatch {
			methods, err := route.GetMethods()
			if err != nil {
				return nil, err
			}
			allMethods = append(allMethods, methods...)
		}
	}
	return allMethods, nil
}
