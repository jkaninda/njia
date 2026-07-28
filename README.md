# njia - A fast, zero-dependency HTTP router for Go

[![CI](https://github.com/jkaninda/njia/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/jkaninda/njia/actions/workflows/ci.yml)
[![Benchmarks](https://github.com/jkaninda/njia/actions/workflows/bench.yml/badge.svg?branch=main)](https://github.com/jkaninda/njia/actions/workflows/bench.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/jkaninda/njia.svg)](https://pkg.go.dev/github.com/jkaninda/njia)
[![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)


> Native routing for modern Go applications, plus a drop-in compatibility layer for migrating from `gorilla/mux`.

**njia** (Swahili: *path, way*) is an HTTP router built around three principles:

* Fast request matching
* Zero dependencies
* Easy migration from `gorilla/mux`

Whether you're building a REST API, reverse proxy, API gateway, or platform, Njia provides a modern router that is fast, introspectable, and safe to use in production.

The main module has **zero** `require` entries and imports **only the Go standard library**. `gorilla/mux` is used exclusively in a separate test module (`internal/difftest`) to verify behavioral compatibility.

## Installation

```bash
go get github.com/jkaninda/njia
```

For `gorilla/mux` compatibility:

```bash
go get github.com/jkaninda/njia/muxcompat
```



## Quickstart

```go
package main

import (
    "log"
    "net/http"

    "github.com/jkaninda/njia"
)

func main() {
    r := njia.New()

    r.GET("/healthz", healthHandler)
    r.GET("/users/{id}", getUser)
    r.GET("/files/{rest...}", serveFile)

    api := r.Group(
        "/api/v1",
        authMiddleware,
        rateLimitMiddleware,
    )

    api.POST("/orders", createOrder)

    log.Fatal(http.ListenAndServe(":8080", r))
}
```

Read a parameter without building a map:

```go
func getUser(w http.ResponseWriter, r *http.Request) {
    id := njia.Param(r, "id")   // no map, no allocation
    ...
}
```

## Routing

### Registering routes

`GET`, `POST`, `PUT`, `PATCH`, `DELETE`, `HEAD` and `OPTIONS` are shorthands for
`Handle`, which takes any method:

```go
r.Handle("REPORT", "/calendars/{id}", reportHandler)
r.HandleFunc("PURGE", "/cache/{key...}", purge)
```

A route registered for `GET` also answers `HEAD`, and the `Allow` header of a
405 lists `HEAD` alongside `GET` accordingly.

#### Every method: `ANY`

`ANY` registers one handler for every method, including verbs no RFC names:

```go
r.ANY("/api/{rest...}", proxy)
```

This is what a reverse proxy needs. A gateway forwards whatever verb the client
sent — WebDAV's `PROPFIND`, a vendor's custom verb — and lets the backend decide
what it accepts; enumerating methods at registration time would make the router
reject a request the proxy would have been happy to forward.

An explicitly registered method always wins over the wildcard, so one route can
serve a verb specially and proxy the rest:

```go
r.GET("/files/{path...}", readFromCache)  // GET comes from here
r.ANY("/files/{path...}", proxy)          // everything else from here
```

A path served by `ANY` never answers 405, because there is no method it rejects.
The `*` sentinel is a registration detail and never appears in an `Allow`
header.

### Options

```go
api.POST("/orders", createOrder, njia.WithName("createOrder"))
```

| Option | Effect |
|---|---|
| `WithName(name)` | Names the route; must be unique. Retrieve with `Router.Route(name)`. |
| `WithMeta(key, value)` | Attaches an arbitrary annotation, surfaced by `Routes()`. |
| `WithHost(patterns...)` | Restricts this route to host patterns, overriding its group's. |
| `WithMiddleware(mw...)` | Wraps only this route, inside any group middleware. |
| `WithPriority(n)` | Orders this route ahead of specificity; lower first. See [Match order](#match-order). |

Registration returns an error instead of panicking — see [Errors](#errors).

### Parameters

`{id}` matches any single non-empty segment. `{rest...}` absorbs the remainder
of the path including slashes, and must be the last segment. A placeholder
carries a name and nothing else.

#### Constraints are rejected, not ignored

```go
err := r.GET("/users/{id:int}", getUser)
// njia: route GET "/users/{id:int}": njia: malformed route pattern:
// "{id:int}" constrains "id", which this router does not support; write {id}
```

A pattern that looks like it filters values can never silently match
everything. Validate in the handler, where a bad value produces a useful 400
rather than falling through to a 404. If you need matching itself to depend on
the value, [`muxcompat`](#migrating-from-gorillamux) accepts gorilla's
`{id:[0-9]+}`.

#### Reading captured values

| Call | Returns | Allocates |
|---|---|---|
| `Param(r, "id")` | The value, or `""`. | no |
| `ParamAt(r, i)` | `(name, value, ok)` in pattern order. | no |
| `NumParams(r)` | How many were captured. | no |
| `AppendParams(r, dst)` | Appends every parameter to `dst`. | no, given capacity |
| `ParamMap(r)` | `map[string]string`, the shape gorilla's `Vars` returned. | yes |
| `RouteOf(r)` | The `*Route` that matched. | no |

`SetParams(req, params...)` attaches parameters to a request, for tests that
invoke a handler directly rather than through the router.

Parameters are captured into a fixed-size array carried by the request context
and spill to the heap only past that size. A static route declares no
parameters and so writes nothing to the context at all.

### Match order

Routes are matched most-specific-first: a static segment beats a wildcard,
which beats a catch-all, resolved position by position from the left. Matching
backtracks, so a static branch that dead-ends never hides a wildcard that would
have matched.

Host specificity is considered after path specificity — see
[Host matching](#host-matching).

#### Overriding it: `WithPriority`

Specificity is the right default — given `/api/v1/{rest...}` and
`/api/{rest...}`, the longer prefix is almost always what should serve
`/api/v1/x`. A gateway assembling routes from user configuration sometimes needs
the opposite, and specificity alone cannot express it:

```go
r.ANY("/api/{rest...}", maintenance, njia.WithPriority(-1))
r.ANY("/api/v1/{rest...}", backend)
// GET /api/v1/x -> maintenance, despite being the less specific pattern
```

Priority is compared before specificity, **lower first**. `DefaultPriority` is
`0`, so a negative value pulls a route ahead of everything unmarked and a
positive one pushes it behind.

Two details worth knowing:

- Routes sharing a path pattern share the lowest priority any of them asked for,
  because ordering picks a pattern before it picks a method.
- Priorities disable the lookup fast paths for the whole table, since those
  answer with the most specific match without consulting other candidates.
  Leaving priority unset everywhere — the default — costs nothing.

### Groups and middleware

A group prefixes patterns and wraps handlers, and nests:

```go
api := r.Group("/api/v1", authMiddleware, rateLimitMiddleware)
v1u := api.Group("/users", auditMiddleware)
v1u.GET("/{id}", getUser)                    // GET /api/v1/users/{id}

api.Prefix()                                 // "/api/v1"
api.Hosts()                                  // host patterns, if restricted
```

Order, outermost first: router middleware, then each enclosing group's
middleware from outer to inner, then the route's own `WithMiddleware`, then the
handler. This is tested, not merely documented.

#### `Use` is not positional

Middleware is resolved when the table is compiled, not when a route is
registered, so where a `Use` call sits among the registrations does not change
what it covers. A route registered before it is wrapped just like one
registered after, and so is a child group created before it:

```go
api := r.Group("/api")
v1 := api.Group("/v1")       // created before the Use
api.Use(auth)                // covers v1 as well
v2 := api.Group("/v2")       // and v2

v1.GET("/orders", list)      // authenticated
v2.GET("/orders", list)      // authenticated
```

`Router.Use` and `Group.Use` behave the same way; the only difference is scope.
This matches `gorilla/mux`, whose middleware ran at match time and so applied
whatever the registration order — code moved across keeps working, and moving a
`Use` call up or down a file can never silently drop authentication.

To wrap only some routes, say so structurally rather than by ordering:

```go
api := r.Group("/api")
api.GET("/public", public)                    // no auth

secure := api.Group("/admin", auth)           // scope is visible here
secure.GET("/settings", settings)
```

or attach it to a single route with `WithMiddleware`.

### Mounting a handler

`Mount` hands every request at or below a prefix to one handler, for every
method — another router, a file server, a debug endpoint:

```go
r.Mount("/debug/pprof", pprofHandler)
r.Mount("/static", http.FileServer(http.Dir("public")))
r.Mount("/legacy", oldRouter)
```

Both the prefix and its subtree are covered, so `/static` and
`/static/css/app.css` both reach the handler, and matching is segment-bounded:
mounting `/api` does not capture `/apiary`.

**The prefix is not stripped.** A proxy needs the path as it arrived, and a
handler that wants it removed can say so, which reads better than a routing rule
that silently rewrites:

```go
r.Mount("/static", http.StripPrefix("/static", fs))
```

A more specific route still wins, which is how an exception is carved out of a
mount:

```go
r.Mount("/api", proxy)
r.GET("/api/health", localHealth)   // served locally, not proxied
```

The remainder is captured under `MountParam`. Because that name is fixed,
mounting and separately registering a *differently named catch-all at the same
position* conflict — `r.Mount("/admin", h)` then `r.GET("/admin/{files...}", x)`
returns `ErrParamConflict`. A plain `{id}` parameter there is fine.

### Router configuration

Every field is off by default. Set them on a router from `New()`, which is the
only supported way to construct one — the zero `Router` has no route table and
panics on registration.

```go
r := njia.New()
r.NotFound = http.HandlerFunc(myNotFound)
r.MethodNotAllowed = http.HandlerFunc(my405)
r.CleanPath = true
r.RedirectTrailingSlash = true
r.RouteInContext = true
```

| Field | Effect when set |
|---|---|
| `NotFound` | Serves unmatched requests. Default: `http.NotFoundHandler`. |
| `MethodNotAllowed` | Serves a path hit with the wrong method. Default: 405 plus an `Allow` header. |
| `CleanPath` | Redirects a non-canonical path to its cleaned form with 301 — `/a//b` → `/a/b`. |
| `RedirectTrailingSlash` | Redirects `/x/` to `/x`, or `/x` to `/x/`, when only the other is registered. Without it, the other form is a 404. |
| `RouteInContext` | Makes `RouteOf` work for static routes too, at one allocation per request. |

`RouteInContext` exists because a static route otherwise attaches nothing to the
request. Leave it off unless handlers actually need the matched route.

---

## Host matching

```go
gw := r.Host("api.example.com", "*.api.example.com")
gw.GET("/orders/{id}", getOrder)

r.Host("{tenant}.app.example.com").GET("/dashboard", dashboard)  // njia.Param(req, "tenant")
r.GET("/healthz", health)                                        // every host
```

### Patterns

Most specific first:

| Pattern | Matches |
|---|---|
| `api.example.com:8443` | exactly this host on this port |
| `api.example.com` | exactly this host, any port |
| `{sub}.example.com` | exactly one leading label, captured as `sub` |
| `*.example.com` | one or more leading labels |
| `{sub...}.example.com` | one or more leading labels, captured |
| `{host...}` | any host, captured whole |
| `*` | any host — also spelled `njia.AnyHost` |

Matching is case-insensitive and ignores a trailing dot, so `API.Example.COM.`
and `api.example.com` are the same name. A pattern that names a port only
matches requests carrying that port; one that does not, ignores the port
entirely.

`WithHost(...)` restricts a single route and overrides its group.
`ValidateHost` checks a pattern without registering anything.

A host parameter is reported by `Routes()` before any path parameter, marked
`InHost` with a `Position` of `-1`, and is read with `njia.Param` like any
other.

### Precedence

Path specificity is decided first, host specificity second, registration order
last. A global `/healthz` therefore stays reachable underneath a per-host
catch-all proxy route — which is exactly how a gateway needs it:

```go
r.GET("/healthz", health)                                  // wins on every host
r.Host("okapi.example.com").GET("/{rest...}", proxyOkapi)  // everything else
```

Within one path pattern, hosts are consulted from most to least specific, and a
variant that does not serve the request's method falls through to a less
specific one.

- A path that exists but not on the requested host is a **404**.
- A path that exists on that host but not for that method is a **405**, and the
  `Allow` header only lists methods that host actually serves.

### Cost

Exact hosts are indexed by name, so a gateway with a thousand virtual hosts
costs one map lookup, not a thousand comparisons. Tables that use no host
constraint at all never read the request's host — the feature costs them
nothing.

---

## Errors

Registration returns errors and never panics:

```go
if err := r.GET("/users/{id", handler); err != nil {
    // njia: route GET "/users/{id": njia: malformed route pattern: ...
}
```

This matters for gateways that build routes from user-supplied configuration: a
bad entry is rejected, not fatal. Paired with [`Swap`](#hot-reload), a whole
table is validated off to the side and installed only if it is sound, so a typo
in someone's YAML can never take the process down.

Routes fixed in code can be checked in one place rather than at every call, with
`Builder.Err()` after registering, or by letting a bad table fail the `Swap`.

### Sentinels

Every failure is a typed sentinel wrapped in a `*RouteError` that names the
offending method and pattern.

| Sentinel | Cause |
|---|---|
| `ErrBadPattern` | Malformed template. |
| `ErrNoLeadingSlash` | Pattern does not start with `/`. |
| `ErrDuplicateRoute` | Same method and pattern registered twice. |
| `ErrDuplicateName` | Two routes given the same name. |
| `ErrParamConflict` | Conflicting parameter names at one position. |
| `ErrCatchAllPosition` | `{rest...}` is not the last segment. |
| `ErrNoHandler` | Route registered without a handler. |
| `ErrEmptyMethod` | Route registered without a method. |
| `ErrBadHost` | Malformed host pattern. |

### `*RouteError`

Exposes `Method`, `Pattern` and `Err`, and implements `Unwrap`:

```go
if errors.Is(err, njia.ErrDuplicateRoute) {
    ...
}

var rerr *njia.RouteError
if errors.As(err, &rerr) {
    log.Printf("bad route %s %s: %v", rerr.Method, rerr.Pattern, rerr.Err)
}
```

`Builder` accumulates errors so a gateway can report every problem in a
configuration file rather than only the first — see [Hot reload](#hot-reload).
`ValidateHost` checks a host pattern without registering anything, so a bad
configuration entry can be rejected before a table is built.

---

## Introspection

```go
for _, ri := range r.Routes() {
    fmt.Println(ri.Method, ri.PathTemplate, ri.Params, ri.Meta)
}

route := r.Route("createOrder")   // by name
fmt.Println(r.String())           // whole table, for start-up logs
```

`RouteInfo` carries `Name`, `Method`, `PathTemplate` as written, `Hosts`,
`Params`, the `Handler` before middleware, and any `Meta` annotations. Each
`ParamInfo` gives `Name`, `Position`, `CatchAll` and `InHost`.

An OpenAPI generator needs nothing else — in particular it never has to
reconstruct a template from a compiled regular expression. Value types are not
part of the pattern, so a generator carries schema information in `WithMeta`.

A `*Route` obtained from `Route(name)` or `RouteOf(req)` exposes the same
information through `Method()`, `Pattern()`, `Hosts()`, `Name()`, `Handler()`,
`Params()` and `Meta(key)`.

### Matching without serving

```go
route, ok := r.Lookup(req)                      // no allocation, no parameters

var buf [8]njia.PathParam
route, params, ok := r.LookupInto(req, buf[:0]) // no allocation, with parameters
```

Useful for authorization checks, metrics labelled by route template, and
anything that needs to know which route *would* serve a request without
serving it.

---

## Hot reload

```go
err := r.Swap(func(b *njia.Builder) error {
    for _, route := range configFromYAML() {
        if err := b.Handle(route.Method, route.Path, route.Handler); err != nil {
            return err
        }
    }
    return nil
})
```

The new table is built and fully validated off to the side. On any error the
running table is untouched. On success it is installed with a single atomic
pointer store; in-flight requests finish against the old table and there is no
lock anywhere on the request path.

### Builder

A `Builder` can also be built and inspected on its own, which lets a gateway
report every problem in a configuration file rather than only the first:

```go
b := njia.NewBuilder()
for _, route := range configFromYAML() {
    _ = b.Handle(route.Method, route.Path, route.Handler)
}
if errs := b.Errs(); len(errs) > 0 {
    return fmt.Errorf("%d bad routes: %w", len(errs), b.Err())
}
```

`Builder` carries the same registration surface as `Router` — `Use`, `Group`,
`Host`, `Handle`, `HandleFunc` and the method shorthands.

---

## Migrating from gorilla/mux

`gorilla/mux` was archived in December 2022 and has been effectively dormant
since. `muxcompat` lets a project move off it with an import rewrite:

```diff
-import "github.com/gorilla/mux"
+import mux "github.com/jkaninda/njia/muxcompat"
```

Nothing else changes. The package reproduces gorilla's exported API and its
observable behavior — route ordering, strict-slash redirects, path cleaning,
`MatchErr` propagation, subrouter matcher inheritance, reverse URL building,
`Walk`, `CORSMethodMiddleware`.

### Including the surprising corners

- `Queries` with an odd number of arguments records an error **and returns
  nil**, so chaining onto it panics. Reproduced, because callers may depend on
  it.
- A host template without a port has the request's port stripped at the first
  colon; a host template with a port does not.
- `Methods()` with no arguments matches nothing.
- `Queries("k", "")` matches the key with any value.
- A capturing group inside a variable pattern panics at registration.

Where njia deliberately differs, it is only by being more robust: a handful of
inputs make gorilla fault at runtime (`nil pointer dereference`, `slice bounds
out of range`) and njia serves them instead. The differential harness treats a
gorilla runtime fault as a gorilla bug and only requires that njia does not
fault differently.

### Not the destination

`muxcompat` is a bridge. It stays published for anyone migrating off gorilla,
but new features go into the native API. Nothing in `muxcompat` imports the
root `njia` package; the two surfaces evolve independently on top of shared
`internal/` packages.

---

## Correctness

Behavior is never written from memory or from documentation prose. Every
gorilla behavior njia reproduces was first observed by running real gorilla.

- **`internal/difftest`** drives both engines with identical route tables and
  identical requests, then compares the matched route, captured variables,
  response status, redirect location, response body, match error, per-route
  build errors and panic behavior.
- **`internal/difftest/vendored`** is gorilla's own test suite, adapted to
  target `muxcompat`. It carries gorilla's BSD-3-Clause header; only test cases
  and fixtures were taken, never implementation code. `OMITTED.md` records the
  handful of white-box tests that could not be expressed through the exported
  API.
- **A property-based generator** builds random route tables and request paths
  covering static paths, wildcards, regular expression constraints, prefixes,
  host templates, methods, queries, headers, schemes, subrouters, overlapping
  routes, percent-encoded and empty and dot segments, very long paths and
  unicode. CI runs 200,000 generated cases per commit.
- **Real route tables** extracted from
  [Okapi](https://github.com/jkaninda/okapi) and
  [Goma Gateway](https://github.com/jkaninda/goma-gateway) are replayed against
  both engines under five router configurations. This is the migration
  acceptance gate.
- **The lookup index is proved inert**: `muxcompat` can be forced onto the
  plain ordered scan, and a test drives every table both ways and requires the
  two to agree on every observable field.
- **Host routing is checked against a reference model**: a deliberately naive
  resolver that scans every route and sorts, run against 400 generated route
  tables over every combination of 11 hosts, 13 paths and 5 methods — about
  286,000 comparisons. It is what caught the specificity bug that let
  `/api/{rest...}` shadow `/api`.
- **Allocation counts are asserted, not hoped for.** Dedicated tests require a
  static match to serve with zero allocations and the tree lookup to capture
  parameters without allocating, so a regression fails the build rather than
  quietly showing up in a benchmark later.

---

## Performance

The native router matches with a segment-indexed prefix tree and a direct map
lookup for fully static patterns. `muxcompat` splits its table: routes that are
static segments plus plain `{name}` wildcards with at most a method filter go
into the tree, everything else stays on an ordered scan, and registration
sequence numbers are compared across the two so gorilla's
first-registered-wins ordering is preserved exactly.

`internal/difftest/bench` compares gorilla/mux, the standard library
`ServeMux`, chi and both njia surfaces at 10, 100 and 1000 routes, across static
hits, parameter hits, deep nested hits, 404 misses, 405 mismatches,
virtual-host routing and table registration.

Run the grid yourself:

```sh
cd internal/difftest
go test -run '^$' -bench . -benchmem -count=6 ./bench/...
go run golang.org/x/perf/cmd/benchstat@latest -col /size <output>
```

### Feature costs

`BenchmarkGateway_*` measures the two proxy features against the same table
registered without them. These are native-only and sit outside the cross-engine
grid, because gorilla has no equivalent of either and a comparison against an
engine doing something different says nothing.

```sh
go test -run '^$' -bench 'BenchmarkGateway_' -benchmem -count=6 ./bench/...
```

Unlike the grid, both sides of each comparison come from one binary, so the
alignment noise described below does not apply and the deltas are readable
directly. Two properties are worth confirming on your own hardware.

### Reading a run

- **`-count=6` and `benchstat` are not optional ceremony.** A single pass is
  noisy enough that machine drift reads as a real regression.
- **A delta under roughly 6% is not attributable.** When comparing two njia
  revisions, read the `gorilla`, `stdlib` and `chi` rows first. Their code is
  identical between revisions, yet they still move by several percent, because
  two different njia binaries shift code alignment around unrelated functions.
  That floor is a property of the binaries, not of the machine, so no amount of
  repetition or interleaving removes it. Promote a delta to real only if it
  clears the band *and* is corroborated — the `Lookup/*` rows, which measure
  matching without `ServeHTTP`, are a good independent check on the `Router_*`
  rows.

The grid also contains `TestGridSanity` and `TestHostGridSanity`, which assert
that every engine really returns 200/404/405 for the scenarios it is
benchmarked on, so no router can look fast by quietly 404ing.

---

## License

Apache-2.0. Test cases and fixtures adapted from gorilla/mux are BSD-3-Clause
and retain their original copyright header; see [`NOTICE`](NOTICE).
