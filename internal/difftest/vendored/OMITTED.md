# Omitted gorilla/mux tests

These test functions from gorilla/mux v1.8.1 were **not** ported into this
package. Each one is a pure white-box unit test (or test helper) that reaches
into unexported implementation details which have no equivalent on the exported
`muxcompat` API surface, so there is no behavior left to assert once the
internals are removed. Everything else from `mux_test.go`, `old_test.go` and
`middleware_test.go` was ported, rewriting white-box assertions in terms of the
exported API wherever the underlying *behavior* was observable.

| Omitted | Source file | Why |
| --- | --- | --- |
| `Test_copyRouteConf` | `mux_test.go` | Constructs `routeConf`, `routeRegexpGroup`, `routeRegexp` and `matcher` values directly and asserts that the unexported `copyRouteConf` helper copies every field. Route-config copying is an internal implementation concern with no exported surface, so the test cannot be expressed through the public API. |
| `testUseEscapedRoute` (helper) plus its six call sites in `TestPath`, `TestPathPrefix`, `TestSchemeHostPath`, `TestQueries`, `TestSubRouter`, `TestStrictSlash` | `mux_test.go` | The helper mutated the unexported `Route.useEncodedPath` field on an *already constructed* `*Route` and re-ran the route test. Only `Router.UseEncodedPath()` is exported, and it cannot be applied retroactively to an existing `*Route`, so the escaped-path re-run is not reachable from outside the package. The primary (non-escaped) assertions of all six tests are still ported and still run; `TestUseEncodedPath`, which exercises `Router.UseEncodedPath()` through the exported API, is ported unchanged. |
| `(*Route).GoString` / `(*routeRegexp).GoString` (helpers) | `mux_test.go` | Debug formatters that print unexported `Route`/`routeRegexp` fields. They are only used for `%#v` in failure messages, and declaring methods on an imported type is not legal Go outside the defining package. Failure messages now use Go's default `%#v` rendering, so no assertion changed. |

## White-box tests that were rewritten rather than omitted

For the record, these gorilla tests touched unexported identifiers but were
kept because their behavior *is* observable through the exported API:

- `TestNamedRoutes` — asserted on `Router.namedRoutes`; now resolves every name
  (including subrouter-registered names) through `Router.Get`.
- `TestWalkSingleDepth` / `TestWalkNested` — read
  `route.matchers[0].(*routeRegexp).template` and `route.regexp.path.template`;
  now use `Route.GetPathTemplate`.
- `TestMiddlewareAdd` — asserted on `Router.middlewares`; now observes each
  registered middleware's invocation count through `Router.ServeHTTP`.
- `TestVariableNames` — read `route.err`; now uses `Route.GetError`.
- `TestRedirectSlash` — read/wrote `route.strictSlash`; now builds routes from
  `StrictSlash(true)` / `StrictSlash(false)` routers and asserts on the
  observable redirect behavior.
- `TestNewRegexp` — built a `*routeRegexp` and inspected the compiled regexp's
  submatches; now builds a route from the same template and inspects the
  matched vars in `Route.GetVarNames` order.
- `TestHeaderMatcher`, `TestMethodMatcher`, `TestSchemeMatcher` (`old_test.go`)
  — instantiated the unexported `headerMatcher` / `methodMatcher` /
  `schemeMatcher` types; now build equivalent routes via `Route.Headers`,
  `Route.Methods` and `Route.Schemes`.
