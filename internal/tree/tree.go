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

// Package tree implements the segment-indexed prefix tree used to match routes
// whose templates consist of static segments, whole-segment wildcards with an
// optional type constraint, and an optional trailing catch-all.
//
// The tree never allocates during lookup: captured parameters are appended to
// a caller-supplied slice and matches are appended to a caller-supplied slice.
// Every matching template is reported rather than only the most specific one,
// so that the caller is free to choose a winner by registration order,
// specificity, or anything else.
package tree

import (
	"errors"
	"fmt"
	"strings"
)

// Kind classifies a template segment.
type Kind uint8

const (
	// KindStatic is a literal segment that must equal the request segment.
	KindStatic Kind = iota
	// KindParam is a whole-segment wildcard matching one non-empty segment.
	KindParam
	// KindCatchAll is a trailing wildcard matching the remainder of the path,
	// including slashes. It must be the last segment.
	KindCatchAll
)

// Check reports whether a captured value satisfies a segment's type
// constraint. A nil Check accepts everything.
type Check func(string) bool

// Segment is one component of a template.
type Segment struct {
	// Kind is the segment's classification.
	Kind Kind
	// Text is the literal text for a static segment, or the parameter name for
	// a wildcard.
	Text string
	// Type names the constraint applied to a wildcard, or is empty when the
	// wildcard is unconstrained. Two wildcards at the same position are the
	// same edge of the tree when their types are equal.
	Type string
	// Check enforces Type. It is nil when Type is empty.
	Check Check
}

// Param is a captured parameter value.
type Param struct {
	// Name is the parameter name from the template.
	Name string
	// Value is the text captured from the request path.
	Value string
}

// Errors reported by Insert.
var (
	// ErrCatchAllPosition reports a catch-all segment that is not last.
	ErrCatchAllPosition = errors.New("njia/tree: catch-all segment must be last")
	// ErrConflict reports a template that cannot coexist with one already in
	// the tree because it names the same position differently.
	ErrConflict = errors.New("njia/tree: conflicting parameter name")
)

// Tree is a prefix tree of route templates. The zero value is ready to use.
//
// It is generic in the value it stores so that a lookup hands the caller its
// own type back: nothing is boxed into an interface on insertion and nothing
// is type-asserted on the request path.
type Tree[T any] struct {
	root      node[T]
	maxParams int
	size      int
}

type node[T any] struct {
	statics   map[string]*node[T]
	params    []edge[T]
	catchAlls []edge[T]
	values    []T
}

// edge is a wildcard transition out of a node.
type edge[T any] struct {
	name  string
	typ   string
	check Check
	n     *node[T]
}

// Len returns the number of values stored in the tree.
func (t *Tree[T]) Len() int { return t.size }

// MaxParams returns the largest number of parameters any stored template
// declares. Callers size their parameter buffers with it.
func (t *Tree[T]) MaxParams() int { return t.maxParams }

// Insert adds value under the given segments. Several values may share one
// template, in which case they are reported together in insertion order.
//
// Insert returns an error rather than panicking on a conflicting template,
// which is what lets a router validate a user-supplied route table without
// risking the process.
func (t *Tree[T]) Insert(segs []Segment, value T) error {
	n := &t.root
	params := 0
	for i, s := range segs {
		switch s.Kind {
		case KindStatic:
			if n.statics == nil {
				n.statics = make(map[string]*node[T], 4)
			}
			next := n.statics[s.Text]
			if next == nil {
				next = &node[T]{}
				n.statics[s.Text] = next
			}
			n = next

		case KindParam:
			params++
			next, err := follow(&n.params, s, i)
			if err != nil {
				return err
			}
			n = next

		case KindCatchAll:
			if i != len(segs)-1 {
				return fmt.Errorf("%w: %q is at position %d of %d", ErrCatchAllPosition, s.Text, i, len(segs))
			}
			params++
			next, err := follow(&n.catchAlls, s, i)
			if err != nil {
				return err
			}
			n = next
		}
	}
	n.values = append(n.values, value)
	t.size++
	if params > t.maxParams {
		t.maxParams = params
	}
	return nil
}

// follow returns the edge for s, creating it when the position does not yet
// carry a wildcard of that type.
func follow[T any](edges *[]edge[T], s Segment, pos int) (*node[T], error) {
	for i := range *edges {
		e := &(*edges)[i]
		if e.typ != s.Type {
			continue
		}
		if e.name != s.Text {
			return nil, fmt.Errorf("%w: position %d is named %q here and %q in an existing route",
				ErrConflict, pos, s.Text, e.name)
		}
		return e.n, nil
	}
	n := &node[T]{}
	*edges = append(*edges, edge[T]{name: s.Text, typ: s.Type, check: s.Check, n: n})
	// Constrained wildcards are tried before unconstrained ones, so that a
	// depth-first walk visits candidates in decreasing specificity and the
	// first leaf it reaches is the best match.
	es := *edges
	for i := len(es) - 1; i > 0; i-- {
		if es[i].check != nil && es[i-1].check == nil {
			es[i], es[i-1] = es[i-1], es[i]
			continue
		}
		break
	}
	return n, nil
}

// Lookup returns the value of the most specific template that matches path,
// with the parameters it captures appended to params.
//
// Specificity is resolved position by position from the left: a static segment
// beats a constrained wildcard, which beats an unconstrained wildcard, which
// beats a catch-all. Wildcards of equal rank are tried in insertion order. The
// walk backtracks, so a static segment that leads to a dead end does not
// prevent a wildcard from matching.
func (t *Tree[T]) Lookup(path string, params []Param) (T, []Param, bool) {
	return t.root.lookup(path, params)
}

func (n *node[T]) lookup(path string, params []Param) (T, []Param, bool) {
	var zero T
	if path == "" {
		if len(n.values) > 0 {
			return n.values[0], params, true
		}
		for i := range n.catchAlls {
			e := &n.catchAlls[i]
			if len(e.n.values) == 0 || (e.check != nil && !e.check("")) {
				continue
			}
			return e.n.values[0], append(params, Param{Name: e.name}), true
		}
		return zero, params, false
	}

	rest := path[1:]
	var seg string
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		seg, rest = rest[:i], rest[i:]
	} else {
		seg, rest = rest, ""
	}

	if n.statics != nil {
		if next := n.statics[seg]; next != nil {
			if v, out, ok := next.lookup(rest, params); ok {
				return v, out, true
			}
		}
	}
	if seg != "" {
		for i := range n.params {
			e := &n.params[i]
			if e.check != nil && !e.check(seg) {
				continue
			}
			if v, out, ok := e.n.lookup(rest, append(params, Param{Name: e.name, Value: seg})); ok {
				return v, out, true
			}
		}
	}
	remainder := path[1:]
	for i := range n.catchAlls {
		e := &n.catchAlls[i]
		if len(e.n.values) == 0 || (e.check != nil && !e.check(remainder)) {
			continue
		}
		return e.n.values[0], append(params, Param{Name: e.name, Value: remainder}), true
	}
	return zero, params, false
}

// Collect appends the values of every template that matches path to dst and
// returns the extended slice. Callers that supply a dst with spare capacity
// avoid all allocation.
func (t *Tree[T]) Collect(path string, dst []T) []T {
	return t.root.collect(path, dst)
}

func (n *node[T]) collect(path string, dst []T) []T {
	if path == "" {
		dst = append(dst, n.values...)
		// A catch-all also matches the empty remainder, so that
		// "/files/{rest...}" accepts a bare "/files".
		for i := range n.catchAlls {
			dst = append(dst, n.catchAlls[i].n.values...)
		}
		return dst
	}

	// A path being matched always begins at a separator; the segment is what
	// follows it up to the next separator.
	rest := path[1:]
	var seg string
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		seg, rest = rest[:i], rest[i:]
	} else {
		seg, rest = rest, ""
	}

	if n.statics != nil {
		if next := n.statics[seg]; next != nil {
			dst = next.collect(rest, dst)
		}
	}
	if seg != "" {
		for i := range n.params {
			e := &n.params[i]
			if e.check == nil || e.check(seg) {
				dst = e.n.collect(rest, dst)
			}
		}
	}
	remainder := path[1:]
	for i := range n.catchAlls {
		e := &n.catchAlls[i]
		if e.check == nil || e.check(remainder) {
			dst = append(dst, e.n.values...)
		}
	}
	return dst
}

// Extract walks segs against path and appends the captured parameters to dst.
// It reports whether path matches the segments. Callers use it to recover the
// parameter values of a template that Collect already reported as matching,
// without re-running a regular expression.
func Extract(segs []Segment, path string, dst []Param) ([]Param, bool) {
	for _, s := range segs {
		if s.Kind == KindCatchAll {
			rest := ""
			if path != "" {
				rest = path[1:]
			}
			if s.Check != nil && !s.Check(rest) {
				return dst, false
			}
			return append(dst, Param{Name: s.Text, Value: rest}), true
		}
		if path == "" || path[0] != '/' {
			return dst, false
		}
		rest := path[1:]
		var seg string
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			seg, rest = rest[:j], rest[j:]
		} else {
			seg, rest = rest, ""
		}
		switch s.Kind {
		case KindStatic:
			if seg != s.Text {
				return dst, false
			}
		case KindParam:
			if seg == "" || (s.Check != nil && !s.Check(seg)) {
				return dst, false
			}
			dst = append(dst, Param{Name: s.Text, Value: seg})
		}
		path = rest
	}
	return dst, path == ""
}
