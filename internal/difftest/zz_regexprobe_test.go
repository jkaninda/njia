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
	"fmt"
	"testing"

	"github.com/jkaninda/njia/internal/difftest"
)

// regexCase is one template plus the targets it should be probed with.
type regexCase struct {
	label   string
	spec    difftest.RouteSpec
	targets []string
}

func pathCase(label, tpl string, targets ...string) regexCase {
	return regexCase{
		label:   label,
		spec:    difftest.RouteSpec{Name: "r", Path: tpl},
		targets: targets,
	}
}

// TestRegexDifferential drives every regex shape through both engines and
// reports divergences rather than only failing, so the whole surface is
// visible in one run.
func TestRegexDifferential(t *testing.T) {
	cases := []regexCase{
		// --- character classes and quantifiers
		pathCase("digits", "/u/{id:[0-9]+}", "/u/42", "/u/abc", "/u/", "/u/4a"),
		pathCase("letters", "/u/{id:[a-z]+}", "/u/abc", "/u/ABC", "/u/a1"),
		pathCase("bounded-rep", "/u/{id:[0-9]{2,4}}", "/u/1", "/u/12", "/u/1234", "/u/12345"),
		pathCase("exact-rep", "/u/{id:[a-z]{3}}", "/u/abc", "/u/ab", "/u/abcd"),
		pathCase("star-can-be-empty", "/u/{id:[0-9]*}", "/u/", "/u/1", "/u/x"),
		pathCase("optional", "/u/{id:[0-9]?}", "/u/", "/u/7", "/u/77"),

		// --- groups
		pathCase("noncapturing-alt", "/u/{id:(?:foo|bar)}", "/u/foo", "/u/bar", "/u/baz"),
		pathCase("capturing-group", "/u/{id:(foo|bar)}", "/u/foo"),
		pathCase("nested-noncapturing", "/u/{id:(?:a(?:b|c))+}", "/u/ab", "/u/abac", "/u/ad"),
		pathCase("case-insensitive-flag", "/u/{id:(?i)abc}", "/u/abc", "/u/ABC", "/u/AbC"),

		// --- dot and slash
		pathCase("dot-any", "/u/{id:.}", "/u/a", "/u/ab", "/u/."),
		pathCase("dot-star", "/u/{id:.*}", "/u/", "/u/a/b", "/u/x"),
		pathCase("dot-plus", "/u/{id:.+}", "/u/a/b/c", "/u/"),
		pathCase("slash-in-pattern", "/u/{id:a/b}", "/u/a/b", "/u/a"),
		pathCase("negated-slash-bounded", "/u/{id:[^/]{2,4}}", "/u/ab", "/u/a", "/u/abcde"),

		// --- braces and escaping inside the pattern
		pathCase("literal-brace-class", "/u/{id:[{]}", "/u/{", "/u/x"),
		pathCase("escaped-dot", "/u/{id:a\\.b}", "/u/a.b", "/u/axb"),
		pathCase("quoted-static-dot", "/a.b/{id}", "/a.b/x", "/axb/x"),
		pathCase("static-regex-meta", "/a+b/{id}", "/a+b/x", "/aab/x"),

		// --- anchors written by the user
		pathCase("caret-in-pattern", "/u/{id:^x}", "/u/x", "/u/^x"),
		pathCase("dollar-in-pattern", "/u/{id:x$}", "/u/x", "/u/x$"),

		// --- multiple variables
		pathCase("two-vars-one-segment", "/u/{a:[0-9]+}-{b:[a-z]+}", "/u/12-ab", "/u/12-", "/u/-ab"),
		pathCase("var-then-static", "/u/{a:[0-9]+}/edit", "/u/12/edit", "/u/ab/edit"),
		pathCase("adjacent-vars", "/u/{a:[0-9]}{b:[a-z]}", "/u/1a", "/u/ab"),

		// --- unicode
		pathCase("unicode-class", "/u/{id:\\p{L}+}", "/u/abc", "/u/ünï", "/u/123"),
		pathCase("unicode-literal", "/ünï/{id:[0-9]+}", "/ünï/42", "/ünï/ab"),

		// --- patterns RE2 rejects
		pathCase("backreference", "/u/{id:(a)\\1}", "/u/aa"),
		pathCase("lookahead", "/u/{id:(?=a)}", "/u/a"),
		pathCase("invalid-class", "/u/{id:[z-a]}", "/u/a"),
		pathCase("empty-pattern", "/u/{id:}", "/u/a"),
		pathCase("empty-name", "/u/{:[0-9]+}", "/u/1"),

		// --- prefix, host and query kinds
		{
			label:   "prefix-regex",
			spec:    difftest.RouteSpec{Name: "r", PathPrefix: "/u/{id:[0-9]+}"},
			targets: []string{"/u/42", "/u/42/more", "/u/ab"},
		},
		{
			label:   "host-regex",
			spec:    difftest.RouteSpec{Name: "r", Host: "{sub:[a-z]+}.example.com", Path: "/x"},
			targets: []string{"/x"},
		},
		{
			label:   "query-regex",
			spec:    difftest.RouteSpec{Name: "r", Path: "/x", Queries: []string{"n", "{n:[0-9]+}"}},
			targets: []string{"/x?n=42", "/x?n=ab", "/x"},
		},
		{
			label:   "headers-regexp",
			spec:    difftest.RouteSpec{Name: "r", Path: "/x", HeadersRegexp: []string{"X-Tag", "^v[0-9]+$"}},
			targets: []string{"/x"},
		},

		// --- capturing groups on every kind and through a subrouter, where the
		// template is rewritten before compiling and the panic text must still
		// match gorilla's byte for byte.
		{
			label:   "capturing-group-prefix",
			spec:    difftest.RouteSpec{Name: "r", PathPrefix: "/u/{id:(foo|bar)}"},
			targets: []string{"/u/foo"},
		},
		{
			label:   "capturing-group-host",
			spec:    difftest.RouteSpec{Name: "r", Host: "{sub:(a|b)}.example.com", Path: "/x"},
			targets: []string{"/x"},
		},
		{
			label:   "capturing-group-query",
			spec:    difftest.RouteSpec{Name: "r", Path: "/x", Queries: []string{"n", "{n:(a|b)}"}},
			targets: []string{"/x?n=a"},
		},
		{
			label: "capturing-group-subrouter",
			spec: difftest.RouteSpec{
				Name:       "r",
				PathPrefix: "/api",
				Sub:        []difftest.RouteSpec{{Name: "s", Path: "/u/{id:(foo|bar)}"}},
			},
			targets: []string{"/api/u/foo"},
		},
		{
			label: "regex-through-subrouter",
			spec: difftest.RouteSpec{
				Name:       "r",
				PathPrefix: "/api",
				Sub:        []difftest.RouteSpec{{Name: "s", Path: "/u/{id:[0-9]+}"}},
			},
			targets: []string{"/api/u/42", "/api/u/ab"},
		},
	}

	total, diverged, crashed := 0, 0, 0
	for _, rc := range cases {
		for _, target := range rc.targets {
			total++
			c := difftest.Case{
				Name:   rc.label + " " + target,
				Routes: []difftest.RouteSpec{rc.spec},
				Target: target,
				Host:   "api.example.com",
				Header: map[string][]string{"X-Tag": {"v7"}},
			}
			if difftest.GorillaCrashes(c) {
				crashed++
				fmt.Printf("  GORILLA-CRASH  %-28s %s\n", rc.label, target)
				continue
			}
			if bad, want, got := difftest.Diverges(c); bad {
				diverged++
				t.Errorf("DIVERGENCE %s %q\n  gorilla: %s\n  njia:    %s", rc.label, target, want, got)
			}
		}
	}
	fmt.Printf("\nregex differential: %d probes, %d divergences, %d gorilla crashes\n",
		total, diverged, crashed)
}
