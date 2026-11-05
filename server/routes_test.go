package server

import "testing"

// Guards the admin endpoints page: fails when the mux and RouteRegistry
// diverge in either direction (study rec 2).
func TestRouteRegistryMatchesMux(t *testing.T) {
	s := &Server{}
	registered := map[string]bool{}
	for _, rt := range s.routes() {
		registered[rt.pattern] = true
	}
	listed := map[string]bool{}
	for _, ri := range RouteRegistry {
		if ri.Group == "Admin" || ri.Group == "Review" {
			continue // registered separately via s.Admin.Register / RegisterAPI
		}
		listed[ri.Method+" "+ri.Path] = true
	}
	for p := range registered {
		if !listed[p] {
			t.Errorf("mux registers %q but RouteRegistry omits it", p)
		}
	}
	for p := range listed {
		if !registered[p] {
			t.Errorf("RouteRegistry lists %q but mux does not register it", p)
		}
	}
}
