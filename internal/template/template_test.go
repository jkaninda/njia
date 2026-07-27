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

package template

import (
	"errors"
	"strings"
	"testing"
)

func TestParseCompilesExpectedPatterns(t *testing.T) {
	tests := []struct {
		name     string
		tpl      string
		kind     Kind
		opts     Options
		wantRe   string
		wantVars []string
		wantRev  string
	}{
		{"static path", "/a/b", KindPath, Options{}, `^/a/b$`, nil, "/a/b"},
		{"escapes metacharacters", "/a.b/c+d", KindPath, Options{}, `^/a\.b/c\+d$`, nil, "/a.b/c+d"},
		{"bare variable", "/a/{id}", KindPath, Options{}, `^/a/(?P<v0>[^/]+)$`, []string{"id"}, "/a/%s"},
		{"constrained variable", "/a/{id:[0-9]+}", KindPath, Options{}, `^/a/(?P<v0>[0-9]+)$`, []string{"id"}, "/a/%s"},
		{"braces inside a constraint", "/a/{id:[0-9]{2,4}}", KindPath, Options{}, `^/a/(?P<v0>[0-9]{2,4})$`, []string{"id"}, "/a/%s"},
		{"partial segment variable", "/f/{n}.json", KindPath, Options{}, `^/f/(?P<v0>[^/]+)\.json$`, []string{"n"}, "/f/%s.json"},
		{"prefix is unanchored at the end", "/a", KindPathPrefix, Options{}, `^/a`, nil, "/a"},
		{"host default differs from path", "{sub}.example.com", KindHost, Options{}, `^(?P<v0>[^.]+)\.example\.com$`, []string{"sub"}, "%s.example.com"},
		{"query default is permissive", "k={v}", KindQuery, Options{}, `^k=(?P<v0>.*)$`, []string{"v"}, "k=%s"},
		{"empty query value means any", "k=", KindQuery, Options{}, `^k=.*$`, nil, "k="},
		{"strict slash makes the slash optional", "/a", KindPath, Options{StrictSlash: true}, `^/a[/]?$`, nil, "/a"},
		{"strict slash trims a trailing slash", "/a/", KindPath, Options{StrictSlash: true}, `^/a[/]?$`, nil, "/a/"},
		{"strict slash is ignored for prefixes", "/a", KindPathPrefix, Options{StrictSlash: true}, `^/a`, nil, "/a"},
		{"strict slash is ignored for hosts", "example.com", KindHost, Options{StrictSlash: true}, `^example\.com$`, nil, "example.com"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tpl, err := Parse(tc.tpl, tc.kind, tc.opts)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.tpl, err)
			}
			if got := tpl.Source; got != tc.wantRe {
				t.Errorf("source = %q, want %q", got, tc.wantRe)
			}
			if got := tpl.Re().String(); got != tc.wantRe {
				t.Errorf("compiled regexp = %q, want %q", got, tc.wantRe)
			}
			if got := strings.Join(tpl.Names(), ","); got != strings.Join(tc.wantVars, ",") {
				t.Errorf("vars = %v, want %v", tpl.Names(), tc.wantVars)
			}
			if tpl.Reverse != tc.wantRev {
				t.Errorf("reverse = %q, want %q", tpl.Reverse, tc.wantRev)
			}
			if tpl.Raw != tc.tpl {
				t.Errorf("Raw = %q, want %q", tpl.Raw, tc.tpl)
			}
		})
	}
}

func TestParseRejectsMalformedTemplates(t *testing.T) {
	tests := []struct {
		name string
		tpl  string
		kind Kind
		opts Options
		want error
	}{
		{"unclosed brace", "/a/{id", KindPath, Options{}, ErrUnbalancedBraces},
		{"unopened brace", "/a/id}", KindPath, Options{}, ErrUnbalancedBraces},
		{"empty placeholder", "/a/{}", KindPath, Options{}, ErrMissingName},
		{"empty constraint", "/a/{id:}", KindPath, Options{}, ErrMissingPattern},
		{"capturing group", "/a/{id:(a|b)}", KindPath, Options{}, ErrCapturingGroup},
		{"query without equals", "k", KindQuery, Options{}, ErrBadQueryTemplate},
		{"duplicate names when rejected", "/{a}/{a}", KindPath, Options{RejectDuplicateNames: true}, ErrDuplicateName},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.tpl, tc.kind, tc.opts)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDuplicateNamesAllowedByDefault(t *testing.T) {
	// gorilla/mux accepts a template that names the same variable twice, so
	// muxcompat must too. Only the native router rejects it.
	tpl, err := Parse("/{id}/{id}", KindPath, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(tpl.Vars) != 2 {
		t.Fatalf("got %d vars, want 2", len(tpl.Vars))
	}
}

func TestExtract(t *testing.T) {
	tpl, err := Parse("/a/{x}/b/{y:[0-9]+}", KindPath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	ok := tpl.Extract("/a/q/b/42", func(name, value string) {
		got = append(got, name+"="+value)
	})
	if !ok || strings.Join(got, ",") != "x=q,y=42" {
		t.Fatalf("Extract = %v ok=%v", got, ok)
	}
	if tpl.Extract("/a/q/b/zz", func(string, string) {}) {
		t.Fatal("Extract matched a value violating its constraint")
	}
}

func TestBuild(t *testing.T) {
	tpl, err := Parse("/a/{x}/b/{y:[0-9]+}", KindPath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := tpl.Build(map[string]string{"x": "q", "y": "42"})
	if err != nil || got != "/a/q/b/42" {
		t.Fatalf("Build = %q, %v", got, err)
	}
	if _, err := tpl.Build(map[string]string{"x": "q"}); err == nil {
		t.Fatal("Build accepted a missing value")
	}
	if _, err := tpl.Build(map[string]string{"x": "q", "y": "nope"}); err == nil {
		t.Fatal("Build accepted a value violating its constraint")
	}
}

func TestBuildRestoresStrictSlashTrailingSlash(t *testing.T) {
	tpl, err := Parse("/a/{x}/", KindPath, Options{StrictSlash: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := tpl.Build(map[string]string{"x": "q"})
	if err != nil || got != "/a/q/" {
		t.Fatalf("Build = %q, %v; want the template's own trailing slash back", got, err)
	}
	if !tpl.EndsInSlash || !tpl.StrictSlash {
		t.Fatalf("EndsInSlash=%v StrictSlash=%v", tpl.EndsInSlash, tpl.StrictSlash)
	}
}

func TestClassificationFlags(t *testing.T) {
	tests := []struct {
		tpl        string
		noVars     bool
		allDefault bool
		static     string
	}{
		{"/a/b", true, true, "/a/b"},
		{"/a/{x}", false, true, "/a/"},
		{"/a/{x:[0-9]+}", false, false, "/a/"},
		{"/{x}/{y}", false, true, "/"},
	}
	for _, tc := range tests {
		t.Run(tc.tpl, func(t *testing.T) {
			tpl, err := Parse(tc.tpl, KindPath, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if tpl.NoVars != tc.noVars || tpl.AllDefault != tc.allDefault || tpl.Static != tc.static {
				t.Fatalf("NoVars=%v AllDefault=%v Static=%q; want %v %v %q",
					tpl.NoVars, tpl.AllDefault, tpl.Static, tc.noVars, tc.allDefault, tc.static)
			}
		})
	}
}
