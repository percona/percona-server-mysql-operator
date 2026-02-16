package fill

import (
	"fmt"
	"reflect"
)

// ByType recursively fills zero or nil fields in the given pointer value `v`
// using provided preset values matched by type. It modifies `v` in place.
func ByType(v any, presets ...any) error {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return fmt.Errorf("nil value")
	}
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("value must be a non-nil pointer")
	}
	fillValue(rv, buildRules(presets...))
	return nil
}

type rules map[reflect.Type]reflect.Value

func buildRules(presets ...any) rules {
	r := make(rules, len(presets))
	for _, p := range presets {
		if p == nil {
			continue
		}
		v := reflect.ValueOf(p)
		r[v.Type()] = v
	}
	return r
}

func fillValue(v reflect.Value, r rules) {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			if ok, def := lookup(v.Type(), r); ok {
				v.Set(def)
			} else {
				return
			}
		}
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			sf := t.Field(i)
			if sf.PkgPath != "" { // not exported
				continue
			}
			fv := v.Field(i)

			setIfZero(fv, r)

			switch fv.Kind() {
			case reflect.Pointer, reflect.Struct, reflect.Slice, reflect.Map, reflect.Interface:
				fillValue(fv, r)
			}
		}

	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			fillValue(v.Index(i), r)
		}

	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			k, val := iter.Key(), iter.Value()

			elem := reflect.New(v.Type().Elem()).Elem()
			elem.Set(val)

			setIfZero(elem, r)

			switch elem.Kind() {
			case reflect.Pointer, reflect.Struct, reflect.Slice, reflect.Map, reflect.Interface:
				fillValue(elem, r)
			}
			v.SetMapIndex(k, elem)
		}

	case reflect.Interface:
		if !v.IsNil() {
			cur := v.Elem()
			tmp := reflect.New(cur.Type()).Elem()
			tmp.Set(cur)
			fillValue(tmp, r)
			v.Set(tmp)
		}
	}
}

func setIfZero(fv reflect.Value, r rules) {
	need := false
	switch fv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		need = fv.IsNil()
	default:
		need = fv.IsZero()
	}
	if !need {
		return
	}
	if ok, def := lookup(fv.Type(), r); ok {
		fv.Set(def)
		return
	}
}

func lookup(t reflect.Type, r rules) (bool, reflect.Value) {
	if dv, ok := r[t]; ok {
		if dv.Type().AssignableTo(t) {
			return true, dv
		}
		if dv.Type().ConvertibleTo(t) {
			return true, dv.Convert(t)
		}
	}

	if t.Kind() == reflect.Pointer {
		elem := t.Elem()
		if dv, ok := r[elem]; ok {
			ptr := reflect.New(elem)
			if dv.Type().AssignableTo(elem) {
				ptr.Elem().Set(dv)
				return true, ptr
			}
			if dv.Type().ConvertibleTo(elem) {
				ptr.Elem().Set(dv.Convert(elem))
				return true, ptr
			}
		}
	}

	if t.Kind() != reflect.Interface {
		if dv, ok := r[reflect.PointerTo(t)]; ok && dv.Kind() == reflect.Pointer && !dv.IsNil() && dv.Type().Elem().AssignableTo(t) {
			return true, dv.Elem()
		}
	}
	return false, reflect.Value{}
}
