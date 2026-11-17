package health

import (
	"bytes"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxPrintDepth bounds deepPrint's nesting, so a cycle fails the print instead of looping.
const maxPrintDepth = 64

// timeType is time.Time, which deepPrint prints through its public methods.
var timeType = reflect.TypeFor[time.Time]()

// deepPrint appends v to b with every field, exported or not, in order,
// through pointers, interfaces (with their dynamic type), slices, arrays and
// maps (keys sorted). Strings print length-prefixed, so a print is
// unambiguous without escaping. A time.Time prints as its UTC instant, offset and zone
// name; no other standard library type is read through unexported fields. It
// reports false for anything it cannot print that way, a func or channel,
// or past maxPrintDepth, so such content is never trusted.
func deepPrint(b []byte, v reflect.Value, depth int) ([]byte, bool) {
	if depth > maxPrintDepth {
		return b, false
	}
	if v.IsValid() && v.Type() == timeType {
		if !v.CanInterface() {
			return b, false
		}
		t := v.Interface().(time.Time)
		name, offset := t.Zone()
		b = t.UTC().AppendFormat(append(b, "time:"...), time.RFC3339Nano)
		b = strconv.AppendInt(append(b, '/'), int64(offset), 10)
		return appendString(append(b, '/'), name), true
	}
	switch v.Kind() {
	case reflect.Invalid:
		b = append(b, '~')
	case reflect.Pointer:
		if v.IsNil() {
			return append(b, "nil"...), true
		}
		return deepPrint(append(b, '&'), v.Elem(), depth+1)
	case reflect.Interface:
		if v.IsNil() {
			return append(b, "nil"...), true
		}
		return deepPrint(appendString(b, v.Elem().Type().String()), v.Elem(), depth+1)
	case reflect.Struct:
		if sealed(v.Type()) {
			return b, false
		}
		return printFields(b, v, -1, depth)
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return append(b, "nil"...), true
		}
		b = append(b, '[')
		for i := range v.Len() {
			var ok bool
			if b, ok = deepPrint(b, v.Index(i), depth+1); !ok {
				return b, false
			}
			b = append(b, ',')
		}
		b = append(b, ']')
	case reflect.Map:
		if v.IsNil() {
			return append(b, "nil"...), true
		}
		entries := make([][]byte, 0, v.Len())
		for it := v.MapRange(); it.Next(); {
			e, ok := deepPrint(nil, it.Key(), depth+1)
			if !ok {
				return b, false
			}
			if e, ok = deepPrint(append(e, '='), it.Value(), depth+1); !ok {
				return b, false
			}
			entries = append(entries, e)
		}
		slices.SortFunc(entries, bytes.Compare)
		b = append(b, "map["...)
		for i, e := range entries {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, e...)
		}
		b = append(b, ']')
	case reflect.String:
		b = appendString(b, v.String())
	case reflect.Bool:
		b = strconv.AppendBool(b, v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b = strconv.AppendInt(b, v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		b = strconv.AppendUint(b, v.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		b = strconv.AppendFloat(b, v.Float(), 'g', -1, 64)
	default:
		return b, false
	}
	return b, true
}

// printFields appends v's fields, but for the one at index skip, as deepPrint does.
func printFields(b []byte, v reflect.Value, skip, depth int) ([]byte, bool) {
	b = append(b, '{')
	for i := range v.NumField() {
		if i == skip {
			continue
		}
		var ok bool
		if b, ok = deepPrint(b, v.Field(i), depth+1); !ok {
			return b, false
		}
		b = append(b, ',')
	}
	return append(b, '}'), true
}

// appendString appends s as its length, a colon and its bytes.
func appendString(b []byte, s string) []byte {
	return append(append(strconv.AppendInt(b, int64(len(s)), 10), ':'), s...)
}

// sealed reports whether t is a standard library struct with an unexported
// field: its state is the library's to keep, so deepPrint never reads it.
func sealed(t reflect.Type) bool {
	first, _, _ := strings.Cut(t.PkgPath(), "/")
	if t.PkgPath() == "" || strings.Contains(first, ".") {
		return false
	}
	for i := range t.NumField() {
		if !t.Field(i).IsExported() {
			return true
		}
	}
	return false
}
