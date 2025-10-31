package marshal

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// MarshalIgnoreOmitEmpty marshals v to JSON ignoring `omitempty` tags.
// Types in passthrough are encoded with standard json.Marshal.
func MarshalIgnoreOmitEmpty(v any, passthrough ...any) ([]byte, error) {
	rules := buildPassRules(passthrough...)
	seen := make(map[uintptr]struct{})
	tree, err := marshalValue(reflect.ValueOf(v), rules, seen)
	if err != nil {
		return nil, err
	}
	return json.Marshal(tree)
}

func marshalValue(rv reflect.Value, rules passRules, seen map[uintptr]struct{}) (any, error) {
	if !rv.IsValid() {
		return nil, nil
	}

	if rules.isPassthrough(rv) {
		b, err := json.Marshal(rv.Interface())
		if err != nil {
			return nil, err
		}
		return json.RawMessage(b), nil
	}

	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return nil, nil
		}
		ptr := rv.Pointer()
		if _, ok := seen[ptr]; ok {
			return nil, errors.New("cycle detected while marshaling struct")
		}
		seen[ptr] = struct{}{}
		defer delete(seen, ptr)
		return marshalValue(rv.Elem(), rules, seen)
	case reflect.Interface:
		if rv.IsNil() {
			return nil, nil
		}
		return marshalValue(rv.Elem(), rules, seen)
	}

	switch rv.Kind() {
	case reflect.Bool:
		return rv.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return rv.Uint(), nil
	case reflect.Float32, reflect.Float64:
		return rv.Float(), nil
	case reflect.String:
		return rv.String(), nil

	case reflect.Slice, reflect.Array:
		n := rv.Len()
		out := make([]any, n)
		for i := range n {
			x, err := marshalValue(rv.Index(i), rules, seen)
			if err != nil {
				return nil, err
			}
			out[i] = x
		}
		return out, nil

	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("map key type %s is not supported by JSON (must be string)", rv.Type().Key())
		}
		if rv.IsNil() {
			return nil, nil
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			k := iter.Key().String()
			val, err := marshalValue(iter.Value(), rules, seen)
			if err != nil {
				return nil, err
			}
			out[k] = val
		}
		return out, nil

	case reflect.Struct:
		return marshalStruct(rv, rules, seen)

	default:
		b, err := json.Marshal(rv.Interface())
		if err != nil {
			return nil, err
		}
		return json.RawMessage(b), nil
	}
}

func marshalStruct(rv reflect.Value, rules passRules, seen map[uintptr]struct{}) (any, error) {
	t := rv.Type()
	out := make(map[string]any, t.NumField())

	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.PkgPath != "" { // unexported
			continue
		}

		name, opts := parseJSONTag(sf.Tag.Get("json"))
		if name == "-" {
			continue
		}
		if name == "" {
			name = sf.Name
		}

		fv := rv.Field(i)

		if opts["inline"] {
			v := derefAll(fv)
			if !v.IsValid() {
				continue
			}
			switch v.Kind() {
			case reflect.Struct:
				objAny, err := marshalStruct(v, rules, seen)
				if err != nil {
					return nil, err
				}
				if obj, ok := objAny.(map[string]any); ok {
					maps.Copy(out, obj)
				}
			case reflect.Map:
				if v.Type().Key().Kind() != reflect.String {
					continue
				}
				objAny, err := marshalValue(v, rules, seen)
				if err != nil {
					return nil, err
				}
				if obj, ok := objAny.(map[string]any); ok {
					maps.Copy(out, obj)
				}
			}
			continue
		}

		val, err := marshalValue(fv, rules, seen)
		if err != nil {
			return nil, err
		}

		if opts["string"] {
			switch v := val.(type) {
			case int64, uint64, float64, bool, string, nil:
				val = toJSONStringScalar(v)
			case json.RawMessage:
				val = json.RawMessage(strconv.AppendQuote(nil, string(v)))
			}
		}

		out[name] = val
	}

	return out, nil
}

func derefAll(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

func parseJSONTag(tag string) (name string, opts map[string]bool) {
	opts = make(map[string]bool)
	if tag == "" {
		return "", opts
	}
	if i := strings.IndexByte(tag, ','); i >= 0 {
		name = tag[:i]
		for p := range strings.SplitSeq(tag[i+1:], ",") {
			if p != "" {
				opts[p] = true
			}
		}
		return name, opts
	}
	return tag, opts
}

func toJSONStringScalar(v any) string {
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		b, _ := json.Marshal(x)
		return string(b)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case string:
		return x
	case nil:
		return "null"
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

type passRules struct {
	exact  map[reflect.Type]struct{}
	ifaces []reflect.Type
}

func (p passRules) isPassthrough(v reflect.Value) bool {
	vt := v.Type()
	if _, ok := p.exact[vt]; ok {
		return true
	}
	return slices.ContainsFunc(p.ifaces, vt.Implements)
}

func buildPassRules(passthrough ...any) passRules {
	r := passRules{exact: make(map[reflect.Type]struct{})}
	addType := func(t reflect.Type) {
		if t == nil {
			return
		}
		if t.Kind() == reflect.Interface {
			r.ifaces = append(r.ifaces, t)
			return
		}
		r.exact[t] = struct{}{}
		if t.Kind() == reflect.Pointer {
			r.exact[t.Elem()] = struct{}{}
		} else {
			r.exact[reflect.PointerTo(t)] = struct{}{}
		}
	}
	for _, p := range passthrough {
		if p == nil {
			continue
		}
		if rt, ok := p.(reflect.Type); ok {
			addType(rt)
			continue
		}
		addType(reflect.TypeOf(p))
	}
	return r
}
