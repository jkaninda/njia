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

package njia

import (
	"errors"
	"fmt"
)

// Errors reported by route registration. Every registration failure is one of
// these, wrapped with context; nothing in this package panics.
var (
	// ErrBadPattern reports a template that could not be parsed, such as one
	// with unbalanced braces or an empty variable name.
	ErrBadPattern = errors.New("njia: malformed route pattern")
	// ErrNoLeadingSlash reports a pattern that does not start with a slash.
	ErrNoLeadingSlash = errors.New("njia: route pattern must start with a slash")
	// ErrDuplicateRoute reports a method and pattern pair that is already
	// registered.
	ErrDuplicateRoute = errors.New("njia: duplicate route")
	// ErrParamConflict reports two routes that put differently named
	// parameters with the same constraint at the same position.
	ErrParamConflict = errors.New("njia: conflicting parameter name")
	// ErrCatchAllPosition reports a catch-all parameter that is not the last
	// segment of the pattern.
	ErrCatchAllPosition = errors.New("njia: catch-all parameter must be last")
	// ErrDuplicateName reports a route name that is already in use.
	ErrDuplicateName = errors.New("njia: duplicate route name")
	// ErrNoHandler reports a route registered without a handler.
	ErrNoHandler = errors.New("njia: route has no handler")
	// ErrEmptyMethod reports a route registered without an HTTP method.
	ErrEmptyMethod = errors.New("njia: route has no method")
	// ErrBadHost reports a host pattern that could not be parsed.
	ErrBadHost = errors.New("njia: malformed host pattern")
)

// RouteError identifies which route a registration error came from, so that a
// gateway loading routes from user configuration can report the offending
// entry rather than just the failure.
type RouteError struct {
	// Method is the HTTP method of the route that failed, if known.
	Method string
	// Pattern is the template of the route that failed.
	Pattern string
	// Err is the underlying cause.
	Err error
}

// Error implements the error interface.
func (e *RouteError) Error() string {
	if e.Method == "" {
		return fmt.Sprintf("njia: route %q: %v", e.Pattern, e.Err)
	}
	return fmt.Sprintf("njia: route %s %q: %v", e.Method, e.Pattern, e.Err)
}

// Unwrap returns the underlying cause.
func (e *RouteError) Unwrap() error { return e.Err }

// routeErr wraps err with the identity of the route it came from.
func routeErr(method, pattern string, err error) error {
	if err == nil {
		return nil
	}
	return &RouteError{Method: method, Pattern: pattern, Err: err}
}
