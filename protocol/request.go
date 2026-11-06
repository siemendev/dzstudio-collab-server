package protocol

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"unicode/utf8"
)

const MaxInt = 2_147_483_647

var (
	idRE          = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	requestIDRE   = regexp.MustCompile(`^[0-9a-z]{16}$`)
	instanceKeyRE = regexp.MustCompile(`^[0-9a-z]{10}$`)
	protocolRE    = regexp.MustCompile(`^(\d{1,5})\.(\d{1,5})$`)
)

func IsID(s string) bool { return idRE.MatchString(s) }

// ParseBody reads a request body as one JSON object. Texts must be valid UTF-8 without lone surrogates, and none may
// be longer than 1000 bytes.
func ParseBody(data []byte) (map[string]any, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		return nil, NewError(BadRequest, "body is not UTF-8")
	}
	if loneSurrogate(data) {
		return nil, NewError(BadRequest, "text with a broken character")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, NewError(BadRequest, "body is not JSON")
	}
	if dec.More() {
		return nil, NewError(BadRequest, "body is more than one JSON value")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, NewError(BadRequest, "body must be a JSON object")
	}
	if path, ok := longText(m, ""); !ok {
		return nil, NewError(BadRequest, "text over 1000 bytes at "+path)
	}
	return m, nil
}

func longText(v any, path string) (string, bool) {
	switch x := v.(type) {
	case string:
		if len(x) > MaxTextBytes {
			if path == "" {
				path = "root"
			}
			return path, false
		}
	case []any:
		for i, e := range x {
			if p, ok := longText(e, path+"["+strconv.Itoa(i)+"]"); !ok {
				return p, false
			}
		}
	case map[string]any:
		for k, e := range x {
			sub := k
			if path != "" {
				sub = path + "." + k
			}
			if p, ok := longText(e, sub); !ok {
				return p, false
			}
		}
	}
	return "", true
}

// loneSurrogate finds \uD800–\uDFFF escapes inside strings that do not form a pair.
func loneSurrogate(data []byte) bool {
	inString := false
	pendingHigh := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			continue
		}
		if c == '"' {
			if pendingHigh {
				return true
			}
			inString = false
			continue
		}
		if c != '\\' {
			if pendingHigh {
				return true
			}
			continue
		}
		if i+1 >= len(data) {
			return false
		}
		esc := data[i+1]
		if esc != 'u' || i+5 >= len(data) {
			if pendingHigh {
				return true
			}
			i++
			continue
		}
		n, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 32)
		i += 5
		if err != nil {
			continue
		}
		switch {
		case n >= 0xD800 && n <= 0xDBFF:
			if pendingHigh {
				return true
			}
			pendingHigh = true
		case n >= 0xDC00 && n <= 0xDFFF:
			if !pendingHigh {
				return true
			}
			pendingHigh = false
		default:
			if pendingHigh {
				return true
			}
		}
	}
	return false
}

// R reads fields of a request object; every violation aborts the request with bad_request.
type R struct {
	M    map[string]any
	path string
}

func NewR(m map[string]any) *R { return &R{M: m} }

func (r *R) name(n string) string {
	if r.path == "" {
		return n
	}
	return r.path + "." + n
}

func (r *R) Has(n string) bool {
	v, ok := r.M[n]
	return ok && v != nil
}

func (r *R) String(n string, maxBytes, minBytes int) string {
	s, ok := r.M[n].(string)
	if !ok {
		BadRequestf("%s must be a text", r.name(n))
	}
	if len(s) > maxBytes || len(s) < minBytes {
		BadRequestf("%s must be %d–%d bytes", r.name(n), minBytes, maxBytes)
	}
	return s
}

func (r *R) OptString(n string, maxBytes int) (string, bool) {
	if !r.Has(n) {
		return "", false
	}
	return r.String(n, maxBytes, 0), true
}

func (r *R) ID(n string) string {
	s, ok := r.M[n].(string)
	if !ok || !idRE.MatchString(s) {
		BadRequestf("%s must be an id", r.name(n))
	}
	return s
}

func (r *R) OptID(n string) (string, bool) {
	if !r.Has(n) {
		return "", false
	}
	return r.ID(n), true
}

func (r *R) RequestID() string {
	s, ok := r.M["requestId"].(string)
	if !ok || !requestIDRE.MatchString(s) {
		BadRequestf("requestId must be 16 characters 0-9 a-z")
	}
	return s
}

func (r *R) InstanceKey() string {
	s, ok := r.M["instanceKey"].(string)
	if !ok || !instanceKeyRE.MatchString(s) {
		BadRequestf("instanceKey must be 10 characters 0-9 a-z")
	}
	return s
}

func (r *R) Int(n string, min, max int64) int64 {
	return ToInt(r.M[n], r.name(n), min, max)
}

func (r *R) OptInt(n string, fallback, min, max int64) int64 {
	if !r.Has(n) {
		return fallback
	}
	return r.Int(n, min, max)
}

func (r *R) Bool(n string) bool {
	v, ok := r.M[n]
	if !ok || v == nil {
		return false
	}
	b, ok := v.(bool)
	if !ok {
		BadRequestf("%s must be true or false", r.name(n))
	}
	return b
}

func (r *R) Array(n string, maxItems int) []any {
	a, ok := r.M[n].([]any)
	if !ok {
		BadRequestf("%s must be a list", r.name(n))
	}
	if len(a) > maxItems {
		BadRequestf("%s has more than %d entries", r.name(n), maxItems)
	}
	return a
}

func (r *R) OptArray(n string, maxItems int) ([]any, bool) {
	if !r.Has(n) {
		return nil, false
	}
	return r.Array(n, maxItems), true
}

func (r *R) Object(n string) *R {
	m, ok := r.M[n].(map[string]any)
	if !ok {
		BadRequestf("%s must be an object", r.name(n))
	}
	return &R{M: m, path: r.name(n)}
}

func (r *R) OptObject(n string) *R {
	if !r.Has(n) {
		return nil
	}
	return r.Object(n)
}

func (r *R) OneOf(n string, values ...string) string {
	s, ok := r.M[n].(string)
	if ok {
		for _, v := range values {
			if s == v {
				return s
			}
		}
	}
	BadRequestf("%s must be one of %v", r.name(n), values)
	return ""
}

// Item gives a reader for an element of a list that must be an object.
func Item(v any, name string) *R {
	m, ok := v.(map[string]any)
	if !ok {
		BadRequestf("%s must be an object", name)
	}
	return &R{M: m, path: name}
}

func ToNumber(v any, name string) float64 {
	n, ok := v.(json.Number)
	if !ok {
		BadRequestf("%s must be a number", name)
	}
	f, err := n.Float64()
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		BadRequestf("%s must be a number", name)
	}
	return f
}

func ToInt(v any, name string, min, max int64) int64 {
	n, ok := v.(json.Number)
	if ok {
		if i, err := strconv.ParseInt(string(n), 10, 64); err == nil && i >= min && i <= max {
			return i
		}
		if f, err := n.Float64(); err == nil && f == math.Trunc(f) && f >= float64(min) && f <= float64(max) {
			return int64(f)
		}
	}
	BadRequestf("%s must be a whole number %d–%d", name, min, max)
	return 0
}

func ToVec3(v any, name string) [3]float64 {
	a, ok := v.([]any)
	if !ok || len(a) != 3 {
		BadRequestf("%s must be 3 numbers", name)
	}
	return [3]float64{ToNumber(a[0], name), ToNumber(a[1], name), ToNumber(a[2], name)}
}

// Pieces reads a list of texts and returns it with its size as written.
func Pieces(v any, name string) ([]string, int) {
	a, ok := v.([]any)
	if !ok {
		BadRequestf("%s must be a list of texts", name)
	}
	out := make([]string, len(a))
	n := 0
	for i, e := range a {
		s, ok := e.(string)
		if !ok {
			BadRequestf("%s must be a list of texts", name)
		}
		out[i] = s
		n += PieceBytes(s)
	}
	return out, n
}

// ParseProtocol checks the protocol field: bad_request if missing or malformed, protocol_unsupported for another
// major version.
func ParseProtocol(v any) (major, minor int) {
	s, ok := v.(string)
	if !ok {
		BadRequestf("protocol missing")
	}
	m := protocolRE.FindStringSubmatch(s)
	if m == nil {
		BadRequestf(`protocol must look like "1.0"`)
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	if major != 1 {
		Fail(ProtocolUnsupported, "this server speaks dzcollab 1")
	}
	return major, minor
}
