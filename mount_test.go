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

// Tests for Mount.

package njia_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jkaninda/njia"
)

// serveReq drives a fully built request, for the cases where the host matters.
func serveReq(t *testing.T, r *njia.Router, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// A mount covers the prefix itself and everything beneath it, for any method.
func TestMountCoversPrefixAndSubtree(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.Mount("/admin", echo("mounted")))

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/admin"},
		{http.MethodGet, "/admin/users"},
		{http.MethodPost, "/admin/users/42/edit"},
		{"PROPFIND", "/admin/deep/nested"},
	} {
		rec := serve(t, r, tc.method, tc.path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s %s: status = %d, want 200", tc.method, tc.path, rec.Code)
		}
	}
}

// The remainder of the path is readable through MountParam.
func TestMountCapturesRemainder(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.Mount("/admin", echo("mounted")))
	assertBody(t, r, http.MethodGet, "/admin/a/b", "mounted "+njia.MountParam+"=a/b")
}

// The prefix is not stripped: the handler sees the path as it arrived.
func TestMountDoesNotStripPrefix(t *testing.T) {
	var got string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.URL.Path })
	r := njia.New()
	mustRegister(t, r.Mount("/static", h))

	serve(t, r, http.MethodGet, "/static/css/app.css")
	if got != "/static/css/app.css" {
		t.Fatalf("handler saw %q, want the unmodified path", got)
	}
}

// Mounting is segment-bounded, so a sibling sharing a string prefix is missed.
func TestMountIsSegmentBounded(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.Mount("/api", echo("mounted")))
	if rec := serve(t, r, http.MethodGet, "/apiary"); rec.Code != http.StatusNotFound {
		t.Fatalf("/apiary = %d, want 404", rec.Code)
	}
}

// A more specific route carves an exception out of a mount, which is what makes
// mounting a proxy and keeping one endpoint local work.
func TestMountYieldsToMoreSpecificRoute(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.Mount("/api", echo("mounted")))
	mustRegister(t, r.GET("/api/health", echo("local")))

	assertBody(t, r, http.MethodGet, "/api/health", "local")
	assertBody(t, r, http.MethodGet, "/api/other", "mounted "+njia.MountParam+"=other")
}

// Mounting at the root serves everything.
func TestMountAtRoot(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.Mount("/", echo("root")))

	if rec := serve(t, r, http.MethodGet, "/"); rec.Code != http.StatusOK {
		t.Errorf("/ = %d, want 200", rec.Code)
	}
	if rec := serve(t, r, "PATCH", "/anything/at/all"); rec.Code != http.StatusOK {
		t.Errorf("/anything/at/all = %d, want 200", rec.Code)
	}
}

// A mount on a group resolves against the group's prefix.
func TestMountOnGroup(t *testing.T) {
	r := njia.New()
	g := r.Group("/api/v1")
	mustRegister(t, g.Mount("/files", echo("files")))

	assertBody(t, r, http.MethodGet, "/api/v1/files/x/y", "files "+njia.MountParam+"=x/y")
	if rec := serve(t, r, http.MethodGet, "/files/x"); rec.Code != http.StatusNotFound {
		t.Fatalf("/files/x = %d, want 404", rec.Code)
	}
}

// Route options apply to a mount like any other registration.
func TestMountAcceptsOptions(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.Mount("/api", echo("mounted"), njia.WithHost("api.example.com")))

	req, _ := http.NewRequest(http.MethodGet, "http://api.example.com/api/x", nil)
	rec := serveReq(t, r, req)
	if rec.Code != http.StatusOK {
		t.Errorf("matching host = %d, want 200", rec.Code)
	}

	req, _ = http.NewRequest(http.MethodGet, "http://other.example.com/api/x", nil)
	if rec := serveReq(t, r, req); rec.Code != http.StatusNotFound {
		t.Errorf("other host = %d, want 404", rec.Code)
	}
}

// The documented conflict: a differently named catch-all at the position a
// mount already named.
func TestMountCatchAllNameConflict(t *testing.T) {
	r := njia.New()
	mustRegister(t, r.Mount("/admin", echo("mounted")))

	if err := r.GET("/admin/{files...}", echo("other")); !errors.Is(err, njia.ErrParamConflict) {
		t.Fatalf("err = %v, want ErrParamConflict", err)
	}
	// A plain parameter at that position is fine.
	if err := r.GET("/admin/{id}", echo("byID")); err != nil {
		t.Fatalf("plain parameter should not conflict, got: %v", err)
	}
}
