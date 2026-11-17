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

// locationType is time.Location, which deepPrint prints by name alone.
var locationType = reflect.TypeFor[time.Location]()

// deepPrint writes v to b with every field, exported or not, through
// pointers, interfaces (with their dynamic type), slices, arrays and maps
// (keys sorted). It reports false for a func, channel or other kind it
// cannot print, or past maxPrintDepth, so such content is never trusted.
func deepPrint(b *strings.Builder, v reflect.Value, depth int) bool {
	if depth > maxPrintDepth {
		return false
	}
	switch v.Kind() {
	case reflect.Invalid:
		b.WriteString("~")
	case reflect.Pointer:
		if v.IsNil() {
			b.WriteString("nil")
			return true
		}
		if v.Type().Elem() == locationType {
			b.WriteString("loc:" + strconv.Quote(v.Elem().FieldByName("name").String()))
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
