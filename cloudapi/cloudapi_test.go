package cloudapi

import (
	"net/http"
	"testing"
)

// The route table is sound: each route once, only a lab's own with
// WhileSuspended or Writes, and every pattern one http.ServeMux takes
// (it panics at a bad or clashing one).
func TestRoutes(t *testing.T) {
	mux := http.NewServeMux()
	seen := map[string]bool{}
	for _, r := range Routes {
		if seen[r.Pattern()] {
			t.Errorf("%s twice", r.Pattern())
		}
		seen[r.Pattern()] = true
		if !r.Lab && (r.WhileSuspended || r.Writes) {
			t.Errorf("%s isn't the lab's own, but has %+v", r.Pattern(), r)
		}
		mux.HandleFunc(r.Pattern(), func(http.ResponseWriter, *http.Request) {})
	}
}

func TestRouteOf(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         string // its pattern, "" for none
	}{
		{"GET", PathLab, "GET " + PathLab},
		{"DELETE", PathLab, "DELETE " + PathLab},
		{"PATCH", PathLab, ""},
		{"POST", PathLabStorageKeys, "POST " + PathLabStorageKeys},
		{"GET", PathNames + "smiths", "GET " + PathNames + "{name}"},
		{"GET", PathNames, ""},
		{"GET", PathNames + "a/b", ""},
		{"GET", "/v1/lab/", ""},
		{"POST", "/update", ""},
	} {
		r, ok := RouteOf(c.method, c.path)
		if got := r.Pattern(); !ok && c.want != "" || ok && got != c.want {
			t.Errorf("RouteOf(%s, %s) = %q, %v; want %q", c.method, c.path, got, ok, c.want)
		}
	}
}
