package protocol

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatF32(t *testing.T) {
	cases := map[float64]string{6512: "6512.0", 0.1: "0.1", 0.0000001: "0.0000001", 210.4: "210.4", -3: "-3.0", 0: "0.0"}
	for in, want := range cases {
		if got := FormatF32(in); got != want {
			t.Errorf("FormatF32(%v) = %s, want %s", in, got, want)
		}
	}
	if got := FormatF32(6512.000001); got != "6512.0" {
		t.Errorf("float32 rounding: %s", got)
	}
}

func TestSerializeWritesRawTextAndTightEscapes(t *testing.T) {
	got := Serialize(O("a", "äöü/€ ", "b", "\"\\\n\x01", "f", []F32{1, 0.5}))
	want := `{"a":"äöü/€` + " " + `","b":"\"\\\n\u0001","f":[1.0,0.5]}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if n := PieceBytes("\"\\\n\x01ä"); n != len(`"\"\\\n\u0001ä"`) {
		t.Fatalf("PieceBytes counts as written: %d", n)
	}
}

func TestTextsAreCutAtCharacterBoundaries(t *testing.T) {
	long := strings.Repeat("ä", 600)
	if s := TruncateUTF8(long, 1000); len(s) != 1000 || !utf8.ValidString(s) {
		t.Fatalf("truncate: %d", len(s))
	}
	if s := TruncateUTF8("a"+long, 1000); len(s) != 999 || !utf8.ValidString(s) {
		t.Fatalf("truncate inside a character: %d", len(s))
	}
	pieces := SplitPieces("x" + strings.Repeat("😀", 1000))
	joined := ""
	for _, pc := range pieces {
		if len(pc) > MaxTextBytes || !utf8.ValidString(pc) {
			t.Fatalf("piece of %d bytes", len(pc))
		}
		joined += pc
	}
	if joined != "x"+strings.Repeat("😀", 1000) {
		t.Fatal("pieces join back")
	}
	if SplitPieces("") != nil {
		t.Fatal("an empty text has no pieces")
	}
	if parts := GroupParts(nil, MB); len(parts) != 1 || len(parts[0]) != 0 {
		t.Fatal("no pieces still make one part")
	}
}

func TestParseBody(t *testing.T) {
	if _, err := ParseBody([]byte(`{"a":"` + strings.Repeat("ä", 501) + `"}`)); err == nil {
		t.Fatal("a text over 1000 bytes is refused")
	}
	if _, err := ParseBody([]byte(`{"a":"\ud83d"}`)); err == nil {
		t.Fatal("a lone surrogate is refused")
	}
	if _, err := ParseBody([]byte(`{"a":"😀 \\ud800"}`)); err != nil {
		t.Fatalf("a pair and an escaped backslash are fine: %v", err)
	}
	if _, err := ParseBody([]byte("\xef\xbb\xbf{\"a\":1}")); err != nil {
		t.Fatal("a BOM is ignored")
	}
	if _, err := ParseBody([]byte(`[1]`)); err == nil {
		t.Fatal("only objects")
	}
}

func TestParseClientVersion(t *testing.T) {
	for _, ok := range []string{"0.4.0", "0.10.0", "999.999.999", "1.0.0"} {
		if v, valid := ParseClientVersion(ok); !valid || v.String() != ok {
			t.Errorf("%q should parse", ok)
		}
	}
	for _, bad := range []string{"", "0.4", "0.4.0-beta", "00.4.0", "0.04.0", "1000.0.0", "v0.4.0", " 0.4.0", "0.4.0\n", "0.4.0.1", "1e2.0.0"} {
		if _, valid := ParseClientVersion(bad); valid {
			t.Errorf("%q should not parse", bad)
		}
	}
	a, _ := ParseClientVersion("0.9.3")
	b, _ := ParseClientVersion("0.10.0")
	if !a.Less(b) || b.Less(a) || a.Less(a) {
		t.Error("0.9.3 < 0.10.0 by number")
	}
}
