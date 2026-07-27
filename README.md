# Njia

**njia** (Swahili: *path, way*) is a zero-dependency HTTP router for Go.

It has two public surfaces:

| Surface | Import path | Purpose |
|---|---|---|
| Native API | `github.com/jkaninda/njia` | The real product. Fast, introspectable, allocation-conscious. |
| Compat API | `github.com/jkaninda/njia/muxcompat` | Drop-in replacement for `github.com/gorilla/mux`. A migration bridge. |

The main module has **zero** `require` entries and imports nothing outside the
standard library. `gorilla/mux` appears only in a separate, test-only module
under `internal/difftest`, which is how behavioral parity is verified.

---

## Native API

```go
r := njia.New()

r.GET("/healthz", healthHandler)
r.GET("/users/{id}", getUser)
r.GET("/files/{rest...}", serveFile)

api := r.Group("/api/v1", authMiddleware, rateLimitMiddleware)
api.POST("/orders", createOrder, njia.WithName("createOrder"))

log.Fatal(http.ListenAndServe(":8080", r))
```

Read a parameter without building a map:

```go
func getUser(w http.ResponseWriter, r *http.Request) {
    id := njia.Param(r, "id")   // no map, no allocation
    ...
}
```

### Registration returns errors, never panics

```go
if err := r.GET("/users/{id", handler); err != nil {
    // njia: route GET "/users/{id": njia: malformed route pattern: ...
}
```

Every failure is a typed sentinel (`ErrBadPattern`, `ErrDuplicateRoute`,
`ErrParamConflict`, `ErrCatchAllPosition`, `ErrBadHost`, …) wrapped in
a `*RouteError` that names the offending method and pattern. This matters for
gateways that build routes from user-supplied configuration: a bad entry is
rejected, not fatal.

### Parameters

`{id}` matches any single non-empty segment, and `{rest...}` absorbs the
remainder of the path including slashes. A placeholder carries a name and
nothing else.

`{id:constraint}` is rejected rather than ignored, so a pattern that looks like
it filters values can never silently match everything:

```go
err := r.GET("/users/{id:int}", getUser)
// njia: route GET "/users/{id:int}": njia: malformed route pattern:
// "{id:int}" constrains "id", which this router does not support; write {id}
```

Validate values in the handler, where a bad one can produce a useful 400 rather
than falling through to a 404. `muxcompat` accepts gorilla's `{id:[0-9]+}`
regular expressions if you need matching to depend on the value.

Routes are matched most-specific-first: a static segment beats a wildcard,
which beats a catch-all, resolved position by position from the left. Matching
backtracks, so a static branch that dead-ends never hides a wildcard that would
have matched.

### Host matching

```go
gw := r.Host("api.example.com", "*.api.example.com")
gw.GET("/orders/{id}", getOrder)

r.Host("{tenant}.app.example.com").GET("/dashboard", dashboard)  // njia.Param(req, "tenant")
r.GET("/healthz", health)                                        // every host
```

Accepted patterns, most specific first:

| Pattern | Matches |
|---|---|
| `api.example.com:8443` | exactly this host on this port |
| `api.example.com` | exactly this host, any port |
| `{sub}.example.com` | exactly one leading label, captured as `sub` |
| `*.example.com` | one or more leading labels |
| `{sub...}.example.com` | one or more leading labels, captured |
| `{host...}` | any host, captured whole |
| `*` | any host |

Matching is case-insensitive and ignores a trailing dot, so `API.Example.COM.`
and `api.example.com` are the same name. A pattern that names a port only
matches requests carrying that port; one that does not, ignores the port
entirely. `WithHost(...)` restricts a single route and overrides its group.
`ValidateHost` checks a pattern without registering anything, so a gateway can
reject a bad configuration entry before building a table.

**Precedence.** Path specificity is decided first, host specificity second,
registration order last. A global `/healthz` therefore stays reachable
underneath a per-host catch-all proxy route — which is exactly how a gateway
needs it:

```go
r.GET("/healthz", health)                                  // wins on every host
r.Host("okapi.example.com").GET("/{rest...}", proxyOkapi)  // everything else
```

Within one path pattern, hosts are consulted from most to least specific, and a
variant that does not serve the request's method falls through to a less
specific one. A path that exists but not on the requested host is a 404; a path
that exists on that host but not for that method is a 405, and the `Allow`
header only lists methods that host actually serves.

Exact hosts are indexed by name, so a gateway with a thousand virtual hosts
costs one map lookup, not a thousand comparisons. Tables that use no host
constraint at all never read the request's host — the feature costs them
nothing.

### Introspection

```go
for _, ri := range r.Routes() {
    fmt.Println(ri.Method, ri.PathTemplate, ri.Params, ri.Meta)
}
```

`RouteInfo` carries the template as written, the host patterns it answers on,
each parameter's name and position (host parameters are reported first, marked
`InHost`), the handler, and any annotations attached with `WithMeta`. An OpenAPI
generator needs nothing else — in particular it never has to reconstruct a
template from a compiled regular expression. Value types are not part of the
pattern, so a generator carries schema information in `WithMeta`.

### Atomic hot reload

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

### Middleware ordering

Router middleware is outermost, then each enclosing group's middleware from
outer to inner, then the route's own middleware, then the handler. This is
tested, not merely documented.

---

## Compat API

`gorilla/mux` was archived in December 2022 and has been effectively dormant
since. `muxcompat` lets a project move off it with an import rewrite:

```diff
-import "github.com/gorilla/mux"
+import mux "github.com/jkaninda/njia/muxcompat"
```

Nothing else changes. The package reproduces gorilla's exported API and its
observable behavior — route ordering, strict-slash redirects, path cleaning,
`MatchErr` propagation, subrouter matcher inheritance, reverse URL building,
`Walk`, `CORSMethodMiddleware` — including the corners that are surprising:

- `Queries` with an odd number of arguments records an error **and returns
  nil**, so chaining onto it panics. Reproduced, because callers may depend on
  it.
- A host template without a port has the request's port stripped at the first
  colon; a host template with a port does not.
- `Methods()` with no arguments matches nothing.
- `Queries("k", "")` matches the key with any value.
- A capturing group inside a variable pattern panics at registration.

Where njia deliberately differs from gorilla, it is only by being more robust:
a handful of inputs make gorilla fault at runtime (`nil pointer dereference`,
`slice bounds out of range`) and njia serves them instead. The differential
harness treats a gorilla runtime fault as a gorilla bug and only requires that
njia does not fault differently.

### Not the destination

`muxcompat` is a bridge. It stays published for anyone migrating off gorilla,
but new features go into the native API. Nothing in `muxcompat` imports the
root `njia` package; the two surfaces evolve independently on top of shared
`internal/` packages.

---

## How correctness is established

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

---

## Performance

Measured with `internal/difftest/bench`, which compares gorilla/mux, the
standard library `ServeMux`, chi and both njia surfaces at 10, 100 and 1000
routes.

The native router matches with a segment-indexed prefix tree and a direct map
lookup for fully static patterns. `muxcompat` splits its table: routes that are
static segments plus plain `{name}` wildcards with at most a method filter go
into the tree, everything else stays on an ordered scan, and registration
sequence numbers are compared across the two so gorilla's
first-registered-wins ordering is preserved exactly.

Run the grid yourself:

```
cd internal/difftest
go test -run '^$' -bench . -benchmem -benchtime=500ms -count=6 ./bench/...
go run golang.org/x/perf/cmd/benchstat@latest -col /size <output>
```

`-count=6` and `benchstat` are not optional ceremony: a single pass is noisy
enough that machine drift reads as a real regression. When comparing two
revisions, check the `stdlib` and `chi` rows first — their code does not change
between njia revisions, so if they moved, the machine moved and nothing can be
attributed to njia.

The grid also contains `TestGridSanity` and `TestHostGridSanity`, which assert
that every engine really returns 200/404/405 for the scenarios it is
benchmarked on, so no router can look fast by quietly 404ing.

---

## Licensing

njia is Apache-2.0. Test cases and fixtures adapted from gorilla/mux are
BSD-3-Clause and retain their original copyright header; see `NOTICE`.
