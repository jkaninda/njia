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
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jkaninda/njia/internal/difftest"
)

var (
	genCases = flag.Int("gen.cases", 12000, "number of generated differential cases")
	genSeed  = flag.Int64("gen.seed", 20260726, "seed for the case generator")
)

// TestGenerated drives randomly generated route tables and requests through
// both engines. The corpus covers static paths, plain and constrained
// variables, path prefixes, host templates, wildcard hosts, methods, queries,
// headers, schemes, subrouters, overlapping and ambiguous routes,
// percent-encoded segments, empty segments, dot segments, very long paths and
// unicode.
func TestGenerated(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(*genSeed))

	var failures int
	for i := 0; i < *genCases; i++ {
		c := genCase(rng, i)
		diverges, want, got := difftest.Diverges(c)
		if !diverges {
			continue
		}
		failures++
		if failures <= 10 {
			spec, _ := json.Marshal(c)
			t.Errorf("divergence in generated case %d\n  spec:    %s\n  gorilla: %s\n  njia:    %s",
				i, spec, want, got)
		}
	}
	if failures > 10 {
		t.Errorf("%d generated cases diverged in total (first 10 shown)", failures)
	}
	t.Logf("compared %d generated cases", *genCases)
}

// TestGeneratedSelfCheck proves the generated corpus is deterministic under
// gorilla alone, so that any divergence reported above is attributable to
// njia and not to the harness.
func TestGeneratedSelfCheck(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(*genSeed ^ 0x5eed))
	n := *genCases / 10
	if n < 100 {
		n = 100
	}
	for i := 0; i < n; i++ {
		c := genCase(rng, i)
		if ok, a, b := difftest.SelfCheck(c); !ok {
			spec, _ := json.Marshal(c)
			t.Fatalf("harness is not deterministic\n  spec:  %s\n  run1:  %s\n  run2:  %s", spec, a, b)
		}
	}
}

// --- generator -------------------------------------------------------------

var (
	staticSegments = []string{
		"a", "b", "users", "articles", "v1", "x-y", "a.b", "%20", "caf%C3%A9",
		"UPPER", "1", "0", "", "..", ".", "a%2Fb", "ünïcode", "very-long-" + strings.Repeat("s", 40),
	}
	varPatterns = []string{"", ":[0-9]+", ":[a-z]+", ":[a-zA-Z0-9_-]+", ":.*", ":[^/]{2,4}"}
	methodPool  = []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}
	headerPool  = []string{"X-One", "X-Two", "Accept", "Content-Type"}
	valuePool   = []string{"1", "2", "yes", "no", "", "application/json"}
	hostLabels  = []string{"api", "www", "a", "example", "com", "org"}
)

func pick[T any](rng *rand.Rand, xs []T) T { return xs[rng.Intn(len(xs))] }

// genPathTemplate builds a path template and returns it along with a request
// path that has a reasonable chance of matching it.
func genPathTemplate(rng *rand.Rand, varSeq *int) string {
	n := 1 + rng.Intn(4)
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte('/')
		switch rng.Intn(10) {
		case 0, 1, 2, 3, 4, 5:
			b.WriteString(pick(rng, staticSegments))
		default:
			*varSeq++
			b.WriteByte('{')
			b.WriteString("v" + strconv.Itoa(*varSeq))
			b.WriteString(pick(rng, varPatterns))
			b.WriteByte('}')
		}
	}
	if rng.Intn(6) == 0 {
		b.WriteByte('/')
	}
	return b.String()
}

func genHostTemplate(rng *rand.Rand, varSeq *int) string {
	n := 2 + rng.Intn(2)
	parts := make([]string, n)
	for i := range parts {
		if rng.Intn(4) == 0 {
			*varSeq++
			parts[i] = "{h" + strconv.Itoa(*varSeq) + pick(rng, varPatterns) + "}"
		} else {
			parts[i] = pick(rng, hostLabels)
		}
	}
	h := strings.Join(parts, ".")
	if rng.Intn(8) == 0 {
		h += ":8080"
	}
	return h
}

func genRoute(rng *rand.Rand, varSeq *int, depth int) difftest.RouteSpec {
	var s difftest.RouteSpec

	switch rng.Intn(10) {
	case 0, 1:
		s.PathPrefix = genPathTemplate(rng, varSeq)
	case 2:
		s.PathPrefix = genPathTemplate(rng, varSeq)
		s.Path = genPathTemplate(rng, varSeq)
	default:
		s.Path = genPathTemplate(rng, varSeq)
	}

	if rng.Intn(6) == 0 {
		s.Host = genHostTemplate(rng, varSeq)
	}
	switch rng.Intn(8) {
	case 0:
		s.Methods = []string{pick(rng, methodPool)}
	case 1:
		s.Methods = []string{pick(rng, methodPool), pick(rng, methodPool)}
	case 2:
		s.MethodsEmpty = true
	}
	if rng.Intn(8) == 0 {
		*varSeq++
		s.Queries = []string{"q" + strconv.Itoa(rng.Intn(3)), "{qv" + strconv.Itoa(*varSeq) + pick(rng, varPatterns) + "}"}
	} else if rng.Intn(16) == 0 {
		s.Queries = []string{"q" + strconv.Itoa(rng.Intn(3)), pick(rng, valuePool)}
	}
	if rng.Intn(10) == 0 {
		s.Headers = []string{pick(rng, headerPool), pick(rng, valuePool)}
	}
	if rng.Intn(16) == 0 {
		s.HeadersRegexp = []string{pick(rng, headerPool), "^[a-z0-9]*$"}
	}
	if rng.Intn(16) == 0 {
		s.Schemes = []string{pick(rng, []string{"http", "https"})}
	}
	if rng.Intn(20) == 0 {
		s.Matcher = pick(rng, []string{"always", "never", "hasQueryX", "pathIsEven"})
	}
	if rng.Intn(40) == 0 {
		s.BuildOnly = true
	}
	if rng.Intn(30) == 0 {
		s.NoHandler = true
	}
	if depth < 2 && rng.Intn(6) == 0 {
		n := 1 + rng.Intn(2)
		for i := 0; i < n; i++ {
			s.Sub = append(s.Sub, genRoute(rng, varSeq, depth+1))
		}
	}
	return s
}

// genRequestPath builds a request path from the same segment pool the
// templates are built from, so that hits and misses are both common.
func genRequestPath(rng *rand.Rand) string {
	n := 1 + rng.Intn(4)
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte('/')
		b.WriteString(pick(rng, staticSegments))
	}
	if rng.Intn(5) == 0 {
		b.WriteByte('/')
	}
	p := b.String()
	if p == "" {
		p = "/"
	}
	return p
}

func genCase(rng *rand.Rand, i int) difftest.Case {
	varSeq := 0
	n := 1 + rng.Intn(5)
	routes := make([]difftest.RouteSpec, n)
	for j := range routes {
		routes[j] = genRoute(rng, &varSeq, 0)
	}

	target := genRequestPath(rng)
	switch rng.Intn(6) {
	case 0:
		target += "?q0=" + pick(rng, valuePool)
	case 1:
		target += "?q1=" + pick(rng, valuePool) + "&q2=" + pick(rng, valuePool)
	case 2:
		target += "?x=1"
	}
	if rng.Intn(10) == 0 {
		target = "http://" + strings.Join([]string{pick(rng, hostLabels), pick(rng, hostLabels)}, ".") + target
	}

	c := difftest.Case{
		Name:   fmt.Sprintf("gen-%d", i),
		Routes: routes,
		Method: pick(rng, methodPool),
		Target: target,
		Flags: difftest.Flags{
			StrictSlash:             rng.Intn(3) == 0,
			SkipClean:               rng.Intn(4) == 0,
			UseEncodedPath:          rng.Intn(4) == 0,
			NotFoundHandler:         rng.Intn(5) == 0,
			MethodNotAllowedHandler: rng.Intn(5) == 0,
			Middleware:              rng.Intn(3),
		},
	}
	if rng.Intn(5) == 0 {
		c.Host = strings.Join([]string{pick(rng, hostLabels), pick(rng, hostLabels), "com"}, ".")
		if rng.Intn(3) == 0 {
			c.Host += ":8080"
		}
	}
	if rng.Intn(6) == 0 {
		c.Header = map[string][]string{
			pick(rng, headerPool): {pick(rng, valuePool)},
		}
	}
	if rng.Intn(8) == 0 {
		c.TLS = true
	}
	if c.Method == http.MethodHead && rng.Intn(2) == 0 {
		c.Method = http.MethodGet
	}
	return c
}
