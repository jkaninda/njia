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

package difftest

import (
	"encoding/json"
	"testing"
)

// Compare runs the case through both engines and fails the test on any
// observable divergence.
func Compare(t *testing.T, c Case) {
	t.Helper()
	if ok, want, got := Diverges(c); ok {
		spec, _ := json.Marshal(c)
		t.Errorf("divergence for case %s\n  spec:    %s\n  gorilla: %s\n  njia:    %s",
			c.Name, spec, want, got)
	}
}

// Diverges reports whether the two engines disagree, returning both results
// rendered for display.
//
// When gorilla raises a runtime fault the comparison is relaxed: a crash is
// not behavior njia is required to reproduce, so the only requirement is that
// njia survives the same input. Deliberate panics — the one gorilla raises for
// a capturing group in a variable pattern — are still compared exactly.
func Diverges(c Case) (diverges bool, gorillaResult, njiaResult string) {
	g := RunGorilla(c)
	n := RunNjia(c)
	gs, ns := g.String(), n.String()
	if IsCrash(g.Panic) {
		// njia is allowed to survive where gorilla faults, and it is allowed to
		// fault identically when the fault originates in the caller — the
		// harness calling a method on the nil that gorilla's Queries
		// contractually returns for an odd argument list, for example. What it
		// may not do is fault differently.
		if n.Panic == "" || n.Panic == g.Panic {
			return false, gs, ns
		}
		return true, gs + " [gorilla crashed; njia crashed differently]", ns
	}
	return gs != ns, gs, ns
}

// GorillaCrashes reports whether gorilla raises a runtime fault on this case.
func GorillaCrashes(c Case) bool { return IsCrash(RunGorilla(c).Panic) }

// SelfCheck runs the case through gorilla twice and reports whether the two
// runs agree. It exists to prove the harness itself is deterministic before
// any njia result is trusted.
func SelfCheck(c Case) (stable bool, first, second string) {
	a := RunGorilla(c).String()
	b := RunGorilla(c).String()
	return a == b, a, b
}
