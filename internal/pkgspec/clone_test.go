package pkgspec

import (
	"reflect"
	"testing"

	"roostack.cloud/roostack/internal/pkgtest"
)

// A clone equals its package and shares nothing with it: no pointer, map
// or slice of one is the other's.
func TestCloneShares(t *testing.T) {
	for _, name := range pkgtest.Names {
		p, err := Load(pkgtest.File(name))
		if err != nil {
			t.Fatal(err)
		}
		c := p.Clone()
		if !reflect.DeepEqual(p, c) {
			t.Errorf("%s: the clone differs", name)
		}
		if path := shared(reflect.ValueOf(p), reflect.ValueOf(c), name); path != "" {
			t.Errorf("the clone shares %s", path)
		}
	}
	if (*Package)(nil).Clone() != nil {
		t.Error("a nil package's clone")
	}
}

// shared is the path of the first pointer, map or slice a and b share.
func shared(a, b reflect.Value, path string) string {
	switch a.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if a.IsNil() {
			return ""
		}
		if (a.Kind() != reflect.Slice || a.Len() > 0) && a.UnsafePointer() == b.UnsafePointer() {
			return path
		}
	}
	switch a.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !a.IsNil() {
			return shared(a.Elem(), b.Elem(), path)
		}
	case reflect.Struct:
		for i := range a.NumField() {
			if p := shared(a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name); p != "" {
				return p
			}
		}
	case reflect.Map:
		for it := a.MapRange(); it.Next(); {
			if p := shared(it.Value(), b.MapIndex(it.Key()), path+"["+it.Key().String()+"]"); p != "" {
				return p
			}
		}
	case reflect.Slice:
		for i := range a.Len() {
			if p := shared(a.Index(i), b.Index(i), path+"[]"); p != "" {
				return p
			}
		}
	}
	return ""
}
