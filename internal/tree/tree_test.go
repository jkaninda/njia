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

package tree

import (
	"errors"
	"sort"
	"strings"
	"testing"
)

// parse turns a compact template into segments: "a" is static, ":a" is a
// wildcard, "*a" is a catch-all, "#a" is a wildcard constrained to digits.
func parse(t *testing.T, tpl string) []Segment {
	t.Helper()
	if tpl == "" {
		return nil
	}
	if tpl[0] != '/' {
		t.Fatalf("template %q must start with a slash", tpl)
	}
	parts := strings.Split(tpl[1:], "/")
	segs := make([]Segment, len(parts))
	for i, p := range parts {
		switch {
		case strings.HasPrefix(p, ":"):
			segs[i] = Segment{Kind: KindParam, Text: p[1:]}
		case strings.HasPrefix(p, "#"):
			segs[i] = Segment{Kind: KindParam, Text: p[1:], Type: "digits", Check: allDigits}
		case strings.HasPrefix(p, "*"):
			segs[i] = Segment{Kind: KindCatchAll, Text: p[1:], Type: "..."}
		default:
			segs[i] = Segment{Kind: KindStatic, Text: p}
		}
	}
	return segs
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func build(t *testing.T, tpls ...string) *Tree[string] {
	t.Helper()
	tr := &Tree[string]{}
	for _, tpl := range tpls {
		if err := tr.Insert(parse(t, tpl), tpl); err != nil {
			t.Fatalf("inserting %q: %v", tpl, err)
		}
	}
	return tr
}

func TestLookupPrefersSpecificity(t *testing.T) {
	tr := build(t,
		"/users/:id",
		"/users/me",
		"/users/#num",
		"/files/*rest",
		"/",
		"/a/:b/c",
		"/a/x/:c",
	)

	tests := []struct {
		path       string
		want       string
		wantParams string
	}{
		{"/users/me", "/users/me", ""},
		{"/users/42", "/users/#num", "num=42"},
		{"/users/bob", "/users/:id", "id=bob"},
		{"/", "/", ""},
		{"/files/a/b/c", "/files/*rest", "rest=a/b/c"},
		{"/files", "/files/*rest", "rest="},
		{"/files/", "/files/*rest", "rest="},
		{"/a/q/c", "/a/:b/c", "b=q"},
		{"/a/x/z", "/a/x/:c", "c=z"},
		// Backtracking: the static "x" branch leads nowhere for this path, so
		// the wildcard branch must still be tried.
		{"/a/x/c", "/a/x/:c", "c=c"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			var buf [8]Param
			v, ps, ok := tr.Lookup(tc.path, buf[:0])
			if !ok {
				t.Fatalf("no match for %q", tc.path)
			}
			if v != tc.want {
				t.Fatalf("matched %q, want %q", v, tc.want)
			}
			if got := renderParams(ps); got != tc.wantParams {
				t.Fatalf("params = %q, want %q", got, tc.wantParams)
			}
		})
	}
}

func TestLookupMisses(t *testing.T) {
	tr := build(t, "/users/:id", "/a/b/c")
	for _, path := range []string{"/users", "/users/", "/users/1/2", "/a/b", "/a/b/c/d", "/nope"} {
		t.Run(path, func(t *testing.T) {
			var buf [8]Param
			if v, _, ok := tr.Lookup(path, buf[:0]); ok {
				t.Fatalf("unexpected match %q", v)
			}
		})
	}
}

func TestCollectReportsEveryMatch(t *testing.T) {
	tr := build(t, "/x/:a", "/x/#a", "/x/y", "/x/*rest")

	names := tr.Collect("/x/9", nil)
	sort.Strings(names)
	want := []string{"/x/#a", "/x/*rest", "/x/:a"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("Collect = %v, want %v", names, want)
	}

	// "y" fails the digits constraint, so only the static, the plain wildcard
	// and the catch-all match it.
	if got := tr.Collect("/x/y", nil); len(got) != 3 {
		t.Fatalf("Collect for a static value returned %d, want 3", len(got))
	}
	if got := tr.Collect("/nope/deep", nil); len(got) != 0 {
		t.Fatalf("Collect for a miss returned %v", got)
	}
}

func TestInsertConflicts(t *testing.T) {
	tr := build(t, "/x/:a")
	err := tr.Insert(parse(t, "/x/:b"), "second")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	// A differently constrained wildcard at the same position is a separate
	// edge, not a conflict.
	if err := tr.Insert(parse(t, "/x/#b"), "typed"); err != nil {
		t.Fatalf("typed wildcard rejected: %v", err)
	}

	err = tr.Insert([]Segment{
		{Kind: KindCatchAll, Text: "rest"},
		{Kind: KindStatic, Text: "after"},
	}, "bad")
	if !errors.Is(err, ErrCatchAllPosition) {
		t.Fatalf("err = %v, want ErrCatchAllPosition", err)
	}
}

func TestSharedTemplateKeepsInsertionOrder(t *testing.T) {
	tr := &Tree[string]{}
	for _, v := range []string{"first", "second", "third"} {
		if err := tr.Insert(parse(t, "/x/:id"), v); err != nil {
			t.Fatal(err)
		}
	}
	got := tr.Collect("/x/1", nil)
	if len(got) != 3 || got[0] != "first" || got[2] != "third" {
		t.Fatalf("Collect = %v", got)
	}
	if tr.Len() != 3 {
		t.Fatalf("Len = %d, want 3", tr.Len())
	}
	if tr.MaxParams() != 1 {
		t.Fatalf("MaxParams = %d, want 1", tr.MaxParams())
	}
}

func TestExtractAgreesWithLookup(t *testing.T) {
	tr := build(t, "/a/:x/b/:y", "/f/*rest")
	segs := parse(t, "/a/:x/b/:y")

	var buf [8]Param
	if _, _, ok := tr.Lookup("/a/1/b/2", buf[:0]); !ok {
		t.Fatal("lookup failed")
	}
	ps, ok := Extract(segs, "/a/1/b/2", buf[:0])
	if !ok || renderParams(ps) != "x=1,y=2" {
		t.Fatalf("Extract = %q ok=%v", renderParams(ps), ok)
	}
	if _, ok := Extract(segs, "/a/1/c/2", buf[:0]); ok {
		t.Fatal("Extract accepted a non-matching path")
	}
	if _, ok := Extract(segs, "/a/1/b", buf[:0]); ok {
		t.Fatal("Extract accepted a short path")
	}

	ps, ok = Extract(parse(t, "/f/*rest"), "/f/a/b", buf[:0])
	if !ok || renderParams(ps) != "rest=a/b" {
		t.Fatalf("catch-all Extract = %q ok=%v", renderParams(ps), ok)
	}
}

func TestLookupDoesNotAllocate(t *testing.T) {
	tr := &Tree[int]{}
	for i := 0; i < 500; i++ {
		if err := tr.Insert(parse(t, "/api/v"+string(rune('a'+i%26))+"/items/:id"), i); err != nil {
			t.Fatal(err)
		}
	}
	buf := make([]Param, 0, 8)
	if n := testing.AllocsPerRun(200, func() {
		_, out, _ := tr.Lookup("/api/va/items/99", buf)
		buf = out[:0]
	}); n != 0 {
		t.Fatalf("Lookup allocated %v times per run, want 0", n)
	}
}

func renderParams(ps []Param) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.Name + "=" + p.Value
	}
	return strings.Join(parts, ",")
}
