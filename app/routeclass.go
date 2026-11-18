package main

import (
	"fmt"
	"net/http"
	"strings"
)

// routeClass decides how a route takes part in the write gate.
type routeClass uint8

const (
	routeUnclassified routeClass = iota
	// routeRead never waits: GET routes, streams and side-effect-free POSTs.
	routeRead
	// routeWrite waits for the global FIFO write gate.
	routeWrite
	// routeOwn serializes itself with a resource lock and is only audited.
	routeOwn
	// routeExempt bypasses the gate entirely (desktop shutdown).
	routeExempt
)

func (c routeClass) String() string {
	switch c {
	case routeRead:
		return "read"
	case routeWrite:
		return "write"
	case routeOwn:
		return "own"
	case routeExempt:
		return "exempt"
	}
	return "unclassified"
}

type routeSpec struct {
	Pattern string
	Method  string
	Class   routeClass
	Op      string
}

// routeMux registers API routes together with their gate class. Every route
// goes through handle, which refuses a route without an explicit class.
type routeMux struct {
	mux   *http.ServeMux
	s     *server
	specs []routeSpec
}

func newRouteMux(s *server) *routeMux { return &routeMux{mux: http.NewServeMux(), s: s} }

func (m *routeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.mux.ServeHTTP(w, r) }

// get registers a read-only GET route.
func (m *routeMux) get(pattern string, h http.HandlerFunc) {
	if !strings.HasPrefix(pattern, "GET ") {
		panic(fmt.Sprintf("route %q: get requires a GET pattern", pattern))
	}
	m.handle(pattern, routeRead, "", h)
}

// handle registers pattern. op is the short Chinese operation label shown
// to other clients while a write holds the gate.
func (m *routeMux) handle(pattern string, class routeClass, op string, h http.HandlerFunc) {
	method, _, ok := strings.Cut(pattern, " ")
	if !ok {
		panic(fmt.Sprintf("route %q: pattern must name its method", pattern))
	}
	switch {
	case class == routeUnclassified:
		panic(fmt.Sprintf("route %q: missing route class", pattern))
	case class != routeRead && op == "":
		panic(fmt.Sprintf("route %q: %s route needs an operation label", pattern, class))
	case method == http.MethodGet && class != routeRead:
		panic(fmt.Sprintf("route %q: GET routes must be read-only", pattern))
	}
	if m.s != nil {
		switch class {
		case routeWrite:
			h = m.s.gated(op, h)
		case routeOwn:
			h = m.s.audited(op, h)
		}
	}
	m.specs = append(m.specs, routeSpec{Pattern: pattern, Method: method, Class: class, Op: op})
	m.mux.HandleFunc(pattern, h)
}
