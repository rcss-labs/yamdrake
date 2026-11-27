package api

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// withEmptyLists makes every nil slice inside *v an empty one, so it encodes
// as [] and not null. It is for stored documents (a parsed service or job)
// that are shown as they are: the parser leaves absent lists nil, the web
// application iterates every list. []byte values are left alone.
func withEmptyLists(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			withEmptyLists(v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				withEmptyLists(v.Field(i))
			}
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		if v.IsNil() {
			v.Set(reflect.MakeSlice(v.Type(), 0, 0))
			return
		}
		for i := range v.Len() {
			withEmptyLists(v.Index(i))
		}
	case reflect.Map:
		// Map values are not addressable: rebuild each one that holds a list.
		for _, key := range v.MapKeys() {
			value := reflect.New(v.Type().Elem()).Elem()
			value.Set(v.MapIndex(key))
			withEmptyLists(value)
			v.SetMapIndex(key, value)
		}
	}
}

// emptyLists returns a copy of the stored document raw (decoded as T) in which
// every absent list is []. A document that is not valid JSON of that shape is
// an error; null (no document yet) stays null.
func emptyLists[T any](raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return raw, nil
	}
	var doc T
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode stored document: %w", err)
	}
	withEmptyLists(reflect.ValueOf(&doc))
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode stored document: %w", err)
	}
	return out, nil
}
