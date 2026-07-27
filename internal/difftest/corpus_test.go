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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jkaninda/njia/internal/difftest"
)

// corpus is a route table extracted from a real project together with a set of
// requests that exercise it.
type corpus struct {
	Name     string               `json:"name"`
	Source   string               `json:"source"`
	Notes    string               `json:"notes"`
	Routes   []difftest.RouteSpec `json:"routes"`
	Requests []corpusRequest      `json:"requests"`
}

type corpusRequest struct {
	Method string              `json:"method"`
	Target string              `json:"target"`
	Host   string              `json:"host,omitempty"`
	Header map[string][]string `json:"header,omitempty"`
	TLS    bool                `json:"tls,omitempty"`
}

// TestCorpusReplay is the migration acceptance gate: the route tables of Okapi
// and Goma Gateway, replayed against both engines under several router
// configurations.
func TestCorpusReplay(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("testdata/corpus/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no corpus files found under testdata/corpus")
	}

	configs := []struct {
		name  string
		flags difftest.Flags
	}{
		{"default", difftest.Flags{}},
		{"strictslash", difftest.Flags{StrictSlash: true}},
		{"skipclean", difftest.Flags{SkipClean: true}},
		{"encodedpath", difftest.Flags{UseEncodedPath: true}},
		{"handlers+middleware", difftest.Flags{NotFoundHandler: true, MethodNotAllowedHandler: true, Middleware: 2}},
	}

	for _, file := range files {
		file := file
		t.Run(filepath.Base(file), func(t *testing.T) {
			t.Parallel()

			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var c corpus
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatalf("parsing %s: %v", file, err)
			}
			if len(c.Routes) == 0 || len(c.Requests) == 0 {
				t.Fatalf("%s: corpus is empty (%d routes, %d requests)", file, len(c.Routes), len(c.Requests))
			}
			t.Logf("%s: %d routes, %d requests, source %s", c.Name, len(c.Routes), len(c.Requests), c.Source)

			var crashes int
			for _, cfg := range configs {
				for i, req := range c.Requests {
					tc := difftest.Case{
						Name:   fmt.Sprintf("%s/%s/#%d %s %s", c.Name, cfg.name, i, req.Method, req.Target),
						Routes: c.Routes,
						Flags:  cfg.flags,
						Method: req.Method,
						Target: req.Target,
						Host:   req.Host,
						Header: req.Header,
						TLS:    req.TLS,
					}
					if difftest.GorillaCrashes(tc) {
						crashes++
					}
					difftest.Compare(t, tc)
				}
			}
			if crashes > 0 {
				t.Logf("%s: gorilla raised a runtime fault on %d of %d comparisons; njia handled all of them",
					c.Name, crashes, len(configs)*len(c.Requests))
			}
		})
	}
}
