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

/*
Package muxcompat is a drop-in replacement for github.com/gorilla/mux.

It reproduces gorilla/mux's exported API and its observable behavior, including
route ordering, strict-slash redirects, path cleaning, method mismatch
reporting, subrouter matcher inheritance and reverse URL building. Migrating is
an import rewrite: replace the import of "github.com/gorilla/mux" with

	mux "github.com/jkaninda/njia/muxcompat"

and leave every call site alone.

The package exists so that projects depending on the archived gorilla/mux can
move to a maintained implementation without touching call sites. It is a
migration bridge, not the destination: new code should use the native API in
github.com/jkaninda/njia, which is faster, allocation-free on the hot path and
exposes route introspection.

# Compatibility

Behavioral parity with gorilla/mux is verified by a differential test harness
that drives both engines with identical route tables and requests and compares
the matched route, captured variables, response status, redirect location and
match error. Where this package is faster than gorilla it is because of the
internal data structures, never because of a semantic shortcut.

Two deliberate differences exist and neither is observable through the public
API:

  - Route variables are captured into a compact slice during matching and the
    map returned by Vars is built on first use. Handlers that never call Vars
    do not pay for the map.
  - Routes that consist only of static segments and plain {name} wildcards,
    optionally with a method filter, are matched with a radix tree; all other
    routes fall back to an ordered scan. Registration order is preserved by
    comparing registration sequence numbers across the two structures.

The deprecated gorilla field Router.KeepContext is not reproduced. It has had
no effect since gorilla adopted the standard library request context.
*/
package muxcompat
