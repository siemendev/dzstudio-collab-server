// Package protocol holds what every dzcollab 1 answer and request has in common: the JSON writer that keeps the
// game's limits (raw non-ASCII, tight escapes, float32 text, no text over 1000 bytes), the request reader and the
// error codes.
package protocol

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	Marker       = "dzcollab 1\n"
	MaxTextBytes = 1000
	MB           = 1_048_576
)

// Obj is a JSON object that keeps the order in which fields were set.
type Obj struct {
	keys []string
	vals map[string]any
}

func NewObj() *Obj { return &Obj{vals: map[string]any{}} }

// O builds an object from alternating keys and values.
func O(kv ...any) *Obj {
	o := NewObj()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

func (o *Obj) Set(k string, v any) *Obj {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
	return o
}

func (o *Obj) Get(k string) (any, bool) {
	v, ok := o.vals[k]
	return v, ok
}

func (o *Obj) Del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, key := range o.keys {
		if key == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *Obj) Has(k string) bool {
	_, ok := o.vals[k]
	return ok
}

// F32 is a float the protocol writes as the shortest decimal text of its float32 value, without exponent.
type F32 float64

// Raw is already serialized JSON.
type Raw string

// Serialize writes a value as JSON the way the protocol wants it.
func Serialize(v any) string {
	var b strings.Builder
	write(&b, v)
	return b.String()
}

// SerializedBytes is the size of a value as written.
func SerializedBytes(v any) int { return len(Serialize(v)) }

// Encode gives the full body of an answer: marker line plus one JSON object.
func Encode(o *Obj) []byte { return []byte(Marker + Serialize(o)) }

func write(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case *Obj:
		if x == nil {
			b.WriteString("null")
			return
		}
		b.WriteByte('{')
		first := true
		for _, k := range x.keys {
			val := x.vals[k]
			if val == nil {
				continue
			}
			if !first {
				b.WriteByte(',')
			}
			first = false
			WriteString(b, k)
			b.WriteByte(':')
			write(b, val)
		}
		b.WriteByte('}')
	case Raw:
		b.WriteString(string(x))
	case string:
		WriteString(b, TruncateUTF8(x, MaxTextBytes))
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case F32:
		b.WriteString(FormatF32(float64(x)))
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			b.WriteString("0")
		} else {
			b.WriteString(strconv.FormatFloat(x, 'f', -1, 64))
		}
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			write(b, e)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			write(b, e)
		}
		b.WriteByte(']')
	case []*Obj:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			write(b, e)
		}
		b.WriteByte(']')
	case []F32:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			write(b, e)
		}
		b.WriteByte(']')
	case [][]string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			write(b, e)
		}
		b.WriteByte(']')
	default:
		b.WriteString("null")
	}
}

const hexDigits = "0123456789abcdef"

// WriteString writes a JSON string escaped as tightly as JSON allows: only \", \\ and control characters
// (\n \r \t \b \f, otherwise \u00XX); everything else raw, also / and non-ASCII.
func WriteString(b *strings.Builder, s string) {
	b.WriteByte('"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' {
			continue
		}
		b.WriteString(s[start:i])
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			b.WriteString(`\u00`)
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xf])
		}
		start = i + 1
	}
	b.WriteString(s[start:])
	b.WriteByte('"')
}

// PieceBytes is the size of a piece as the server writes it: with quotes, escaped tightly.
func PieceBytes(p string) int {
	n := len(p) + 2
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t' || c == '\b' || c == '\f':
			n++
		case c < 0x20:
			n += 5
		}
	}
	return n
}

func PiecesBytes(ps []string) int {
	n := 0
	for _, p := range ps {
		n += PieceBytes(p)
	}
	return n
}

// TruncateUTF8 cuts a text to at most max bytes, never inside a character.
func TruncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// SplitPieces splits a text into pieces of at most 1000 bytes, only at character boundaries. An empty text has no
// pieces.
func SplitPieces(s string) []string {
	var out []string
	for len(s) > 0 {
		if len(s) <= MaxTextBytes {
			out = append(out, s)
			break
		}
		cut := MaxTextBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return out
}

// GroupParts groups pieces into parts of at most max bytes (counted as written). At least one part.
func GroupParts(pieces []string, max int) [][]string {
	parts := [][]string{}
	cur := []string{}
	n := 0
	for _, p := range pieces {
		size := PieceBytes(p)
		if n+size > max && len(cur) > 0 {
			parts = append(parts, cur)
			cur = []string{}
			n = 0
		}
		cur = append(cur, p)
		n += size
	}
	return append(parts, cur)
}

// FormatF32 gives the shortest decimal text that yields the same float32, never with an exponent, always with a
// decimal point.
func FormatF32(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "0.0"
	}
	f := float32(v)
	if f == 0 {
		return "0.0"
	}
	s := strconv.FormatFloat(float64(f), 'f', -1, 32)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}
