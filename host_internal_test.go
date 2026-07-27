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
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/jkaninda/njia/internal/tree"
)

// --- host pattern unit tests -----------------------------------------------

func TestParseHostPatternClassification(t *testing.T) {
	tests := []struct {
		pattern string
		kind    hostKind
		name    string
		literal string
		suffix  string
		port    string
	}{
		{"example.com", hostExact, "", "example.com", "", ""},
		{"EXAMPLE.com", hostExact, "", "example.com", "", ""},
		{"example.com:8443", hostExact, "", "example.com", "", "8443"},
		{"*.example.com", hostSuffixWild, "", "", ".example.com", ""},
		{"*.example.com:443", hostSuffixWild, "", "", ".example.com", "443"},
		{"{s}.example.com", hostLabel, "s", "", ".example.com", ""},
		{"{s...}.example.com", hostSuffixWild, "s", "", ".example.com", ""},
		{"{h...}", hostAny, "h", "", "", ""},
		{"*", hostAny, "", "", "", ""},
		{"[::1]", hostExact, "", "[::1]", "", ""},
		{"[::1]:8080", hostExact, "", "[::1]", "", "8080"},
	}
	for _, tc := range tests {
		t.Run(tc.pattern, func(t *testing.T) {
			p, err := parseHostPattern(tc.pattern)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if p.kind != tc.kind || p.name != tc.name || p.literal != tc.literal ||
				p.suffix != tc.suffix || p.port != tc.port {
				t.Fatalf("got kind=%d name=%q literal=%q suffix=%q port=%q",
					p.kind, p.name, p.literal, p.suffix, p.port)
			}
			if p.raw != tc.pattern {
				t.Fatalf("raw = %q", p.raw)
			}
		})
	}
}

func TestHostPatternScoreOrdering(t *testing.T) {
	// Written from least to most specific; the scores must be strictly
	// increasing in the same order.
	ordered := []string{
		"*",
		"*.com",
		"*.example.com",
		"{s}.com",
		"{s}.example.com",
		"example.com",
		"api.example.com",
		"api.example.com:8443",
	}
	var prev uint64
	for i, s := range ordered {
		p, err := parseHostPattern(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if i > 0 && p.score <= prev {
			t.Fatalf("%q scores %d, which is not above the previous %d", s, p.score, prev)
		}
		prev = p.score
	}
}

func TestSplitAuthority(t *testing.T) {
	tests := []struct{ in, host, port string }{
		{"example.com", "example.com", ""},
		{"example.com:8080", "example.com", "8080"},
		{"[::1]:8080", "[::1]", "8080"},
		{"[::1]", "[::1]", ""},
		{"::1", "::1", ""},
		{"", "", ""},
		{"example.com:", "example.com", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			host, port := splitAuthority(tc.in)
			if host != tc.host || port != tc.port {
				t.Fatalf("got (%q, %q), want (%q, %q)", host, port, tc.host, tc.port)
			}
		})
	}
}

func TestRequestAuthorityDropsTrailingDot(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	req.Host = "example.com.:8080"
	host, port := requestAuthority(req)
	if host != "example.com" || port != "8080" {
		t.Fatalf("got (%q, %q)", host, port)
	}
}

func TestEqualFoldAndLower(t *testing.T) {
	if !equalFold("ExAmPle.COM", "example.com") {
		t.Error("equalFold failed on mixed case")
	}
	if equalFold("example.com", "example.co") {
		t.Error("equalFold matched different lengths")
	}
	if equalFold("example.com", "example.con") {
		t.Error("equalFold matched different strings")
	}
	// A byte that differs from a letter only in the case bit must not fold to
	// it: '_' is '?'|0x20 and '[' is '{'|0x20.
	if equalFold("_", "?") || equalFold("[", "{") {
		t.Error("equalFold folded a non-letter")
	}
	if got := lower("already"); got != "already" {
		t.Errorf("lower = %q", got)
	}
	if got := lower("MiXeD"); got != "mixed" {
		t.Errorf("lower = %q", got)
	}
	if !hasUpper("aBc") || hasUpper("abc") {
		t.Error("hasUpper is wrong")
	}
}

// reference model
//
// The router answers a request through a prefix tree, an exact-host map and
// ordered wildcard buckets. The reference below answers the same request by
// scanning everything and sorting, which is slow but obviously correct. Any
// disagreement is a bug in the fast structures.

type refVariant struct {
	host     *hostPattern
	seq      int
	byMethod map[string]string
}

type refEntry struct {
	segs     []tree.Segment
	score    uint64
	minSeq   int
	variants []*refVariant
}

type refTable struct {
	entries map[string]*refEntry
}

func newRefTable() *refTable { return &refTable{entries: map[string]*refEntry{}} }

// add mirrors one successful registration.
func (rt *refTable) add(t *testing.T, name, method, pattern string, hosts []string, seq int) {
	t.Helper()
	segs, _, err := parsePattern(pattern)
	if err != nil {
		t.Fatalf("reference cannot parse %q: %v", pattern, err)
	}
	key := patternKey(segs)
	e := rt.entries[key]
	if e == nil {
		e = &refEntry{segs: segs, score: specificity(segs), minSeq: seq}
		rt.entries[key] = e
	}

	pats := []*hostPattern{nil}
	if len(hosts) > 0 {
		pats = pats[:0]
		for _, h := range hosts {
			p, err := parseHostPattern(h)
			if err != nil {
				t.Fatalf("reference cannot parse host %q: %v", h, err)
			}
			if p.kind == hostAny && p.name == "" {
				p = nil // "*" is the same as no constraint
			}
			pats = append(pats, p)
		}
	}
	for _, p := range pats {
		var v *refVariant
		for _, existing := range e.variants {
			if sameRefHost(existing.host, p) {
				v = existing
				break
			}
		}
		if v == nil {
			v = &refVariant{host: p, seq: seq, byMethod: map[string]string{}}
			e.variants = append(e.variants, v)
		}
		v.byMethod[method] = name
	}
}

func sameRefHost(a, b *hostPattern) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.id() == b.id()
}

// resolve returns the matched route name and the status the router should
// produce.
func (rt *refTable) resolve(path, host, port, method string) (string, int) {
	type cand struct{ e *refEntry }
	var cands []cand
	for _, e := range rt.entries {
		if _, ok := tree.Extract(e.segs, path, nil); ok {
			cands = append(cands, cand{e})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i].e, cands[j].e
		if a.score != b.score {
			return a.score > b.score
		}
		return a.minSeq < b.minSeq
	})

	pathMatched := false
	for _, c := range cands {
		vs := append([]*refVariant(nil), c.e.variants...)
		sort.SliceStable(vs, func(i, j int) bool {
			si, sj := refHostScore(vs[i].host), refHostScore(vs[j].host)
			if si != sj {
				return si > sj
			}
			return vs[i].seq < vs[j].seq
		})
		for _, v := range vs {
			if v.host != nil {
				if _, ok := v.host.match(host, port, nil); !ok {
					continue
				}
			}
			pathMatched = true
			if name, ok := v.byMethod[method]; ok {
				return name, http.StatusOK
			}
			if method == http.MethodHead {
				if name, ok := v.byMethod[http.MethodGet]; ok {
					return name, http.StatusOK
				}
			}
		}
	}
	if pathMatched {
		return "", http.StatusMethodNotAllowed
	}
	return "", http.StatusNotFound
}

// refHostScore ranks a variant's host, with the unconstrained variant last.
func refHostScore(p *hostPattern) int64 {
	if p == nil {
		return -1
	}
	return int64(p.score)
}

// --- generated comparison ---------------------------------------------------

func TestHostRoutingMatchesReferenceModel(t *testing.T) {
	const tables = 400
	rng := rand.New(rand.NewSource(20260726))

	hostPool := []string{
		"", "*", "api.example.com", "www.example.com", "api.example.com:8443",
		"*.example.com", "*.api.example.com", "{sub}.example.com",
		"{sub...}.example.com", "{h...}", "shop.example.org", "EXAMPLE.com",
	}
	pathPool := []string{
		"/", "/healthz", "/api", "/api/v1", "/api/{id}", "/api/{id}",
		"/api/v1/users", "/api/v1/users/{id}", "/{rest...}", "/api/{rest...}",
		"/a/{b}/c", "/a/b/c", "/static/{path...}",
	}
	methodPool := []string{"GET", "POST", "PUT", "DELETE"}
	reqHosts := []string{
		"api.example.com", "api.example.com:8443", "api.example.com:80",
		"www.example.com", "a.b.example.com", "x.api.example.com",
		"shop.example.org", "other.net", "API.EXAMPLE.COM", "example.com",
		"deep.sub.example.com",
	}
	reqPaths := []string{
		"/", "/healthz", "/api", "/api/v1", "/api/7", "/api/abc",
		"/api/v1/users", "/api/v1/users/9", "/a/b/c", "/a/z/c",
		"/static/js/app.js", "/nope/deep", "/api/x/y/z",
	}

	for table := 0; table < tables; table++ {
		r := New()
		ref := newRefTable()
		seq := 0

		n := 1 + rng.Intn(8)
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("r%d", i)
			method := methodPool[rng.Intn(len(methodPool))]
			pattern := pathPool[rng.Intn(len(pathPool))]

			var hosts []string
			switch rng.Intn(4) {
			case 0:
				// no host constraint
			case 1:
				if h := hostPool[rng.Intn(len(hostPool))]; h != "" {
					hosts = []string{h}
				}
			default:
				for k := 0; k < 1+rng.Intn(2); k++ {
					if h := hostPool[rng.Intn(len(hostPool))]; h != "" {
						hosts = append(hosts, h)
					}
				}
			}

			opts := []RouteOption{WithName(name)}
			if len(hosts) > 0 {
				opts = append(opts, WithHost(hosts...))
			}
			err := r.Handle(method, pattern, namedHandler(name), opts...)
			if err != nil {
				// A rejected registration is not part of the table, so the
				// reference must not learn about it either.
				if !errors.Is(err, ErrDuplicateRoute) && !errors.Is(err, ErrParamConflict) {
					t.Fatalf("unexpected registration error for %s %s hosts=%v: %v", method, pattern, hosts, err)
				}
				continue
			}
			ref.add(t, name, method, pattern, hosts, seq)
			seq++
		}

		for _, host := range reqHosts {
			for _, path := range reqPaths {
				for _, method := range append(methodPool, "HEAD") {
					wantName, wantCode := ref.resolve(path, hostOf(host), portOf(host), method)

					req := httptest.NewRequest(method, path, nil)
					req.Host = host
					rec := httptest.NewRecorder()
					r.ServeHTTP(rec, req)

					gotName := strings.TrimSpace(rec.Body.String())
					if rec.Code != http.StatusOK {
						gotName = ""
					}
					if rec.Code != wantCode || gotName != wantName {
						t.Fatalf("table %d: %s %s host=%q\n  router:    code=%d name=%q\n  reference: code=%d name=%q\n  routes:\n%s",
							table, method, path, host, rec.Code, gotName, wantCode, wantName, r.String())
					}
				}
			}
		}
	}
}

func namedHandler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(name))
	})
}

func hostOf(authority string) string {
	h, _ := splitAuthority(authority)
	return h
}

func portOf(authority string) string {
	_, p := splitAuthority(authority)
	return p
}

// TestVariantOrderingIsMaintained checks the ordered buckets directly, since a
// mis-ordered insert would only show up as a subtle precedence bug.
func TestVariantOrderingIsMaintained(t *testing.T) {
	r := New()
	// Registered from least to most specific, so every insert has to move.
	for _, h := range []string{"*.com", "*.example.com", "{s}.com", "{s}.example.com"} {
		if err := r.Host(h).GET("/x", namedHandler(h)); err != nil {
			t.Fatal(err)
		}
	}
	e := r.table().static["/x"]
	if e == nil {
		t.Fatal("no entry for /x")
	}
	var got []string
	for _, v := range e.wild {
		got = append(got, v.host.raw)
	}
	want := "{s}.example.com,{s}.com,*.example.com,*.com"
	if strings.Join(got, ",") != want {
		t.Fatalf("wild order = %v, want %v", got, want)
	}
	for i := 1; i < len(e.wild); i++ {
		if e.wild[i-1].host.score < e.wild[i].host.score {
			t.Fatalf("bucket is not ordered by score at %d", i)
		}
	}
}

func TestHostFreeTableSkipsAuthorityEntirely(t *testing.T) {
	r := New()
	for i := 0; i < 5; i++ {
		if err := r.GET(fmt.Sprintf("/r%d", i), namedHandler("x")); err != nil {
			t.Fatal(err)
		}
	}
	tbl := r.table()
	if tbl.hasHosts {
		t.Fatal("a table with no host constraints reports hasHosts")
	}
	req := httptest.NewRequest("GET", "/r1", nil)
	req.Host = "SHOULD.NOT.MATTER"
	if h, p := tbl.authority(req); h != "" || p != "" {
		t.Fatalf("authority = (%q, %q), want it not to be computed", h, p)
	}

	if err := r.Host("a.com").GET("/hosted", namedHandler("h")); err != nil {
		t.Fatal(err)
	}
	if !r.table().hasHosts {
		t.Fatal("a table with a host constraint does not report hasHosts")
	}
}
