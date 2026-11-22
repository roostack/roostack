package pkgspec

import "reflect"

// Clone is a deep copy of the package, sharing nothing with it, for a
// package kept in memory and handed to code that may change its copy.
func (p *Package) Clone() *Package {
	if p == nil {
		return nil
	}
	return deepCopy(reflect.ValueOf(p)).Interface().(*Package)
}

// deepCopy copies v and everything it points to. A package is plain data:
// exported fields, maps, slices, pointers, and the scalars, lists and maps
// YAML decodes into an `any` (TestCloneShares checks every field is
// reached).
func deepCopy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		c := reflect.New(v.Type().Elem())
		c.Elem().Set(deepCopy(v.Elem()))
		return c
	case reflect.Struct:
		c := reflect.New(v.Type()).Elem()
		for i := range v.NumField() {
			c.Field(i).Set(deepCopy(v.Field(i)))
		}
		return c
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		c := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			c.SetMapIndex(it.Key(), deepCopy(it.Value()))
		}
		return c
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		c := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			c.Index(i).Set(deepCopy(v.Index(i)))
		}
		return c
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		c := reflect.New(v.Type()).Elem()
		c.Set(deepCopy(v.Elem()))
		return c
	default:
		return v
	}
}
