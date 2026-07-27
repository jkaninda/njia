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
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jkaninda/njia/internal/template"
)

// regexpKind mirrors template.Kind for the parts of a request a route can
// constrain.
type regexpKind int

const (
	regexpKindPath regexpKind = iota
	regexpKindPrefix
	regexpKindHost
	regexpKindQuery
)

func (k regexpKind) templateKind() template.Kind {
	switch k {
	case regexpKindPrefix:
		return template.KindPathPrefix
	case regexpKindHost:
		return template.KindHost
	case regexpKindQuery:
		return template.KindQuery
	default:
		return template.KindPath
	}
}

// routeRegexp is a compiled host, path or query template together with the
// options it was compiled under. It satisfies the matcher interface.
type routeRegexp struct {
	tpl  *template.Template
	kind regexpKind
	opts routeOptions
	// wildcardHostPort reports that the host template says nothing about the
	// port, in which case the port is stripped from the request host before
	// matching. A template that does mention a port is matched against the
	// host as received.
	wildcardHostPort bool
}

// routeOptions are the per-route flags inherited from the router.
type routeOptions struct {
	strictSlash    bool
	useEncodedPath bool
}

// newRouteRegexp compiles tpl for the given kind.
func newRouteRegexp(tpl string, kind regexpKind, opts routeOptions) (*routeRegexp, error) {
	t, err := template.Parse(tpl, kind.templateKind(), template.Options{
		StrictSlash:    opts.strictSlash,
		UseEncodedPath: opts.useEncodedPath,
	})
	if errors.Is(err, template.ErrCapturingGroup) {
		// gorilla panics here and callers rely on the panic to surface the
		// mistake at registration time, so muxcompat reproduces it. The native
		// njia router returns this as an error instead.
		//
		// The text is gorilla's verbatim, not this package's own wording: a
		// caller that recovers and inspects the message must not be able to
		// tell the two implementations apart.
		panic(fmt.Sprintf("route %s contains capture groups in its regexp. ", tpl) +
			"Only non-capturing groups are accepted: e.g. (?:pattern) instead of (pattern)")
	}
	if err != nil {
		return nil, fmt.Errorf("mux: %w", err)
	}
	rr := &routeRegexp{tpl: t, kind: kind, opts: opts}
	rr.wildcardHostPort = kind == regexpKindHost && !strings.Contains(t.Source, ":")
	return rr, nil
}

// varNames returns the ordered variable names of the compiled template.
func (rr *routeRegexp) varNames() []string {
	out := make([]string, len(rr.tpl.Vars))
	for i, v := range rr.tpl.Vars {
		out[i] = v.Name
	}
	return out
}

// Match reports whether the request satisfies this template.
func (rr *routeRegexp) Match(req *http.Request, match *RouteMatch) bool {
	switch rr.kind {
	case regexpKindHost:
		return rr.tpl.Match(rr.host(req))
	case regexpKindQuery:
		return rr.tpl.Match(rr.queryValue(req))
	default:
		return rr.tpl.Match(requestPath(req, rr.opts.useEncodedPath))
	}
}

// queryValue extracts the "key=value" pair this template constrains from the
// request's raw query, or the empty string when the key is absent.
func (rr *routeRegexp) queryValue(req *http.Request) string {
	key := strings.SplitN(rr.tpl.Raw, "=", 2)[0]
	val, ok := findFirstQueryKey(req.URL.RawQuery, key)
	if !ok {
		return ""
	}
	return key + "=" + val
}

// url renders the template from values.
func (rr *routeRegexp) url(values map[string]string) (string, error) {
	s, err := rr.tpl.Build(values)
	if err != nil {
		return "", fmt.Errorf("mux: %w", err)
	}
	return s, nil
}

// requestPath returns the path a route should be matched against.
func requestPath(req *http.Request, encoded bool) string {
	if encoded {
		return req.URL.EscapedPath()
	}
	return req.URL.Path
}

// host returns the request host this template should be matched against.
//
// When the template says nothing about a port, the port is removed by cutting
// at the first colon. That is deliberately naive — it is what gorilla does,
// and it means a bracketed IPv6 literal such as "[::1]:8080" reduces to "[".
// Host templates are not usable with IPv6 literals in either implementation;
// the behavior is reproduced so that nothing that matches under gorilla stops
// matching here.
func (rr *routeRegexp) host(req *http.Request) string {
	host := getHost(req)
	if rr.wildcardHostPort {
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
	}
	return host
}

// getHost returns the request host, preferring an absolute request URI.
func getHost(req *http.Request) string {
	if req.URL.IsAbs() {
		return req.URL.Host
	}
	return req.Host
}

// findFirstQueryKey returns the value of the first occurrence of key in the raw
// query string, percent-decoded.
func findFirstQueryKey(rawQuery, key string) (string, bool) {
	for len(rawQuery) > 0 {
		pair := rawQuery
		if i := strings.IndexAny(pair, "&;"); i >= 0 {
			pair, rawQuery = pair[:i], pair[i+1:]
		} else {
			rawQuery = ""
		}
		if pair == "" {
			continue
		}
		var value string
		if i := strings.Index(pair, "="); i >= 0 {
			pair, value = pair[:i], pair[i+1:]
		}
		if len(pair) < len(key) {
			continue
		}
		decodedKey, err := url.QueryUnescape(pair)
		if err != nil || decodedKey != key {
			continue
		}
		decodedValue, err := url.QueryUnescape(value)
		if err != nil {
			continue
		}
		return decodedValue, true
	}
	return "", false
}

// routeRegexpGroup collects the host, path and query templates of a route so
// that variables from all of them can be extracted in one place.
type routeRegexpGroup struct {
	host    *routeRegexp
	path    *routeRegexp
	queries []*routeRegexp
}

// setMatch captures every variable the route declares into the match and,
// when strict-slash handling applies, installs a redirect handler.
func (g *routeRegexpGroup) setMatch(req *http.Request, m *RouteMatch, r *Route) {
	if g.host != nil {
		g.host.tpl.Extract(g.host.host(req), m.setVar)
	}

	path := requestPath(req, r.useEncodedPath)
	if g.path != nil {
		if g.path.tpl.Extract(path, m.setVar) && g.path.tpl.StrictSlash {
			hasSlash := strings.HasSuffix(path, "/")
			wantSlash := strings.HasSuffix(g.path.tpl.Raw, "/")
			if hasSlash != wantSlash {
				u := *req.URL
				if hasSlash {
					u.Path = u.Path[:len(u.Path)-1]
				} else {
					u.Path += "/"
				}
				m.Handler = http.RedirectHandler(u.String(), http.StatusMovedPermanently)
			}
		}
	}

	for _, q := range g.queries {
		q.tpl.Extract(q.queryValue(req), m.setVar)
	}
}

// copyGroup returns a copy of the group whose queries slice is independent of
// the receiver's.
func copyGroup(g routeRegexpGroup) routeRegexpGroup {
	out := routeRegexpGroup{host: g.host, path: g.path}
	if len(g.queries) > 0 {
		out.queries = make([]*routeRegexp, len(g.queries))
		copy(out.queries, g.queries)
	}
	return out
}
