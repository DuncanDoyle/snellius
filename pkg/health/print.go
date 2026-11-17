package health

import (
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

// deepPrint writes v to b with every field, exported or not, through
// pointers, interfaces (with their dynamic type), slices, arrays and maps
// (keys sorted). A time.Time prints as its UTC instant, offset and zone name;
// no other standard library type is read through unexported fields. It
// reports false for anything it cannot print that way, a func or channel,
// or past maxPrintDepth, so such content is never trusted.
func deepPrint(b *strings.Builder, v reflect.Value, depth int) bool {
	if depth > maxPrintDepth {
		return false
	}
	if v.IsValid() && v.Type() == timeType {
		if !v.CanInterface() {
			return false
		}
		t := v.Interface().(time.Time)
		name, offset := t.Zone()
		b.WriteString("time:" + t.UTC().Format(time.RFC3339Nano) + "/" + strconv.Itoa(offset) + "/" + strconv.Quote(name))
		return true
	}
	switch v.Kind() {
	case reflect.Invalid:
		b.WriteString("~")
	case reflect.Pointer:
		if v.IsNil() {
			b.WriteString("nil")
			return true
		}
		b.WriteByte('&')
		return deepPrint(b, v.Elem(), depth+1)
	case reflect.Interface:
		if v.IsNil() {
			b.WriteString("nil")
			return true
		}
		b.WriteString(v.Elem().Type().String())
		return deepPrint(b, v.Elem(), depth+1)
	case reflect.Struct:
		if sealed(v.Type()) {
			return false
		}
		b.WriteByte('{')
		for i := range v.NumField() {
			b.WriteString(v.Type().Field(i).Name + ":")
			if !deepPrint(b, v.Field(i), depth+1) {
				return false
			}
			b.WriteByte(',')
		}
		b.WriteByte('}')
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			b.WriteString("nil")
			return true
		}
		b.WriteByte('[')
		for i := range v.Len() {
			if !deepPrint(b, v.Index(i), depth+1) {
				return false
			}
			b.WriteByte(',')
		}
		b.WriteByte(']')
	case reflect.Map:
		if v.IsNil() {
			b.WriteString("nil")
			return true
		}
		entries := make([]string, 0, v.Len())
		for it := v.MapRange(); it.Next(); {
			var e strings.Builder
			if !deepPrint(&e, it.Key(), depth+1) {
				return false
			}
			e.WriteByte('=')
			if !deepPrint(&e, it.Value(), depth+1) {
				return false
			}
			entries = append(entries, e.String())
		}
		slices.Sort(entries)
		b.WriteString("map[" + strings.Join(entries, ",") + "]")
	case reflect.String:
		b.WriteString(strconv.Quote(v.String()))
	case reflect.Bool:
		b.WriteString(strconv.FormatBool(v.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.WriteString(strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		b.WriteString(strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		b.WriteString(strconv.FormatFloat(v.Float(), 'g', -1, 64))
	default:
		return false
	}
	return true
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
