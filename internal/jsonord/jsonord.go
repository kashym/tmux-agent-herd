// Package jsonord decodes and re-encodes JSON while preserving object key
// order, so editing a user's settings file produces a minimal diff.
package jsonord

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Object is an ordered JSON object.
type Object struct {
	Keys []string
	M    map[string]any
}

// NewObject returns an empty object.
func NewObject() *Object { return &Object{M: map[string]any{}} }

// Get returns a value.
func (o *Object) Get(k string) (any, bool) { v, ok := o.M[k]; return v, ok }

// Set sets a value, appending the key if new.
func (o *Object) Set(k string, v any) {
	if _, ok := o.M[k]; !ok {
		o.Keys = append(o.Keys, k)
	}
	o.M[k] = v
}

// Decode parses JSON. Objects become *Object, arrays []any, numbers
// json.Number.
func Decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := NewObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k := kt.(string)
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return tok, nil
	}
}

// Encode writes v as 2-space indented JSON with a trailing newline.
func Encode(v any) []byte {
	var b strings.Builder
	write(&b, v, 0)
	b.WriteByte('\n')
	return []byte(b.String())
}

func scalar(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimRight(buf.String(), "\n")
}

func write(b *strings.Builder, v any, depth int) {
	ind := strings.Repeat("  ", depth+1)
	end := strings.Repeat("  ", depth)
	switch t := v.(type) {
	case *Object:
		if len(t.Keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range t.Keys {
			b.WriteString(ind + scalar(k) + ": ")
			write(b, t.M[k], depth+1)
			if i < len(t.Keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(end + "}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, e := range t {
			b.WriteString(ind)
			write(b, e, depth+1)
			if i < len(t)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(end + "]")
	default:
		b.WriteString(scalar(v))
	}
}
