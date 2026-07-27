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
	"errors"
	"net/http"
	"regexp"
)

// methodMatcher restricts a route to a set of HTTP methods.
type methodMatcher []string

// Match reports whether the request method is one of the route's methods.
func (m methodMatcher) Match(r *http.Request, _ *RouteMatch) bool {
	return matchInArray(m, r.Method)
}

// schemeMatcher restricts a route to a set of URL schemes.
type schemeMatcher []string

// Match reports whether the request scheme is one of the route's schemes.
func (m schemeMatcher) Match(r *http.Request, _ *RouteMatch) bool {
	scheme := r.URL.Scheme
	// Server requests are parsed from the request line and the Host header, so
	// the scheme is usually empty. Infer it from the connection.
	if scheme == "" {
		if r.TLS == nil {
			scheme = "http"
		} else {
			scheme = "https"
		}
	}
	return matchInArray(m, scheme)
}

// headerMatcher restricts a route to requests carrying the given headers. An
// empty expected value requires only that the header be present.
type headerMatcher map[string]string

// Match reports whether the request carries every required header.
func (m headerMatcher) Match(r *http.Request, _ *RouteMatch) bool {
	return matchMapWithString(m, r.Header)
}

// headerRegexpMatcher restricts a route to requests whose headers match the
// given patterns.
type headerRegexpMatcher map[string]*regexp.Regexp

// Match reports whether every required header matches its pattern.
func (m headerRegexpMatcher) Match(r *http.Request, _ *RouteMatch) bool {
	return matchMapWithRegex(m, r.Header)
}

// matchInArray reports whether value is present in arr.
func matchInArray(arr []string, value string) bool {
	for _, v := range arr {
		if v == value {
			return true
		}
	}
	return false
}

// matchMapWithString reports whether toMatch satisfies every entry of toCheck.
// Keys are canonicalised, because everything it is used on is a header set.
func matchMapWithString(toCheck map[string]string, toMatch map[string][]string) bool {
	for k, v := range toCheck {
		values := toMatch[http.CanonicalHeaderKey(k)]
		if values == nil {
			return false
		}
		if v == "" {
			continue
		}
		found := false
		for _, value := range values {
			if v == value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// matchMapWithRegex reports whether toMatch satisfies every pattern in
// toCheck. Keys are canonicalised, as for matchMapWithString.
func matchMapWithRegex(toCheck map[string]*regexp.Regexp, toMatch map[string][]string) bool {
	for k, v := range toCheck {
		values := toMatch[http.CanonicalHeaderKey(k)]
		if values == nil {
			return false
		}
		found := false
		for _, value := range values {
			if v.MatchString(value) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// errOddPairs reports a variadic key/value list with an odd number of
// elements.
var errOddPairs = errors.New("mux: number of parameters must be multiple of 2")

// mapFromPairsToString converts a flat key/value list to a map.
func mapFromPairsToString(pairs ...string) (map[string]string, error) {
	if len(pairs)%2 != 0 {
		return nil, errOddPairs
	}
	m := make(map[string]string, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return m, nil
}

// mapFromPairsToRegex converts a flat key/pattern list to a map of compiled
// patterns.
func mapFromPairsToRegex(pairs ...string) (map[string]*regexp.Regexp, error) {
	if len(pairs)%2 != 0 {
		return nil, errOddPairs
	}
	m := make(map[string]*regexp.Regexp, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		re, err := regexp.Compile(pairs[i+1])
		if err != nil {
			return nil, err
		}
		m[pairs[i]] = re
	}
	return m, nil
}
