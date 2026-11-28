package api

import (
	"reflect"
	"unicode/utf8"
)

// Text that cannot be stored (NUL and invalid UTF-8 are errors in PostgreSQL)
// or that only confuses logs and screens (other control characters) is refused
// at the door with 400 invalid, once, for every route, instead of field by
// field. A struct field of a request body may opt out with a `text` tag:
//
//	text:"multiline"  tab, newline and carriage return are allowed (XML, PEM)
//	text:"any"        not checked (passwords, which are hashed, never stored)

// hasControlText reports whether s has invalid UTF-8, NUL, a C0 or C1 control
// character or DEL. With multiline, tab, newline and carriage return pass.
func hasControlText(s string, multiline bool) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		switch {
		case multiline && (r == '\t' || r == '\n' || r == '\r'):
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return true
		}
	}
	return false
}

// controlProblem walks a decoded request body and returns a message naming the
// first field with refused text ("" when there is none). It never repeats the
// value.
func controlProblem(v any) string {
	return walkText(reflect.ValueOf(v), "request body", false)
}

func walkText(v reflect.Value, field string, multiline bool) string {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return ""
		}
		return walkText(v.Elem(), field, multiline)
	case reflect.String:
		if hasControlText(v.String(), multiline) {
			return field + " contains a control character or invalid text"
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return ""
		}
		for i := range v.Len() {
			if p := walkText(v.Index(i), field, multiline); p != "" {
				return p
			}
		}
	case reflect.Map:
		for iter := v.MapRange(); iter.Next(); {
			if p := walkText(iter.Key(), field, multiline); p != "" {
				return p
			}
			if p := walkText(iter.Value(), field, multiline); p != "" {
				return p
			}
		}
	case reflect.Struct:
		t := v.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := f.Tag.Get("json")
			if name == "" || name == "-" {
				name = f.Name
			} else if comma := indexComma(name); comma >= 0 {
				name = name[:comma]
			}
			switch f.Tag.Get("text") {
			case "any":
				continue
			case "multiline":
				if p := walkText(v.Field(i), name, true); p != "" {
					return p
				}
			default:
				if p := walkText(v.Field(i), name, false); p != "" {
					return p
				}
			}
		}
	}
	return ""
}

func indexComma(s string) int {
	for i := range len(s) {
		if s[i] == ',' {
			return i
		}
	}
	return -1
}
