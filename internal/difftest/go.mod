// Module difftest is a separate, test-only module so that the root njia module
// keeps zero require entries. It depends on the engines njia is compared
// against: gorilla/mux for behavior, chi and the standard library for the
// benchmark grid.
module github.com/jkaninda/njia/internal/difftest

go 1.23

require (
	github.com/gorilla/mux v1.8.1
	github.com/jkaninda/njia v0.0.0
)

require github.com/go-chi/chi/v5 v5.1.0

replace github.com/jkaninda/njia => ../../
