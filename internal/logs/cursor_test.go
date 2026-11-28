package logs

import (
	"strings"
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	in := map[int64]int64{0: 3, 7: 120, 9000000000: 1}
	c, err := encodeCursor(in)
	if err != nil {
		t.Fatalf("encodeCursor: %v", err)
	}
	out, err := decodeCursor(c)
	if err != nil {
		t.Fatalf("decodeCursor: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("round trip = %v, want %v", out, in)
	}
	for leg, seq := range in {
		if out[leg] != seq {
			t.Fatalf("leg %d = %d, want %d", leg, out[leg], seq)
		}
	}
	// Equal positions always encode to the same string.
	again, _ := encodeCursor(map[int64]int64{9000000000: 1, 7: 120, 0: 3})
	if again != c {
		t.Fatalf("encoding is not canonical: %q vs %q", again, c)
	}
}

func TestEmptyCursorMeansStart(t *testing.T) {
	m, err := decodeCursor("")
	if err != nil || len(m) != 0 {
		t.Fatalf("decodeCursor(\"\") = %v, %v", m, err)
	}
}

func TestMalformedCursorIsAnErrorNotAPanic(t *testing.T) {
	good, _ := encodeCursor(map[int64]int64{1: 5, 2: 6})
	cases := map[string]Cursor{
		"not base64":        "!!!not base64!!!",
		"wrong version":     Cursor(encodeRaw("v9;1:5")),
		"truncated":         good[:len(good)-2],
		"garbage text":      Cursor(encodeRaw("hello")),
		"negative seq":      Cursor(encodeRaw("v1;1:-5")),
		"zero seq":          Cursor(encodeRaw("v1;1:0")),
		"negative leg":      Cursor(encodeRaw("v1;-1:5")),
		"duplicate leg":     Cursor(encodeRaw("v1;1:5,1:6")),
		"unsorted":          Cursor(encodeRaw("v1;2:5,1:6")),
		"overflow":          Cursor(encodeRaw("v1;1:99999999999999999999")),
		"empty entry":       Cursor(encodeRaw("v1;1:5,,2:6")),
		"too long":          Cursor(strings.Repeat("A", maxCursorLen+1)),
		"too many entries":  Cursor(encodeRaw("v1;" + manyEntries(maxCursorLegs+1))),
		"trailing comma":    Cursor(encodeRaw("v1;1:5,")),
		"missing separator": Cursor(encodeRaw("v1;15")),
	}
	for name, c := range cases {
		if _, err := decodeCursor(c); err == nil {
			t.Errorf("%s: decodeCursor accepted %q", name, c)
		}
	}
}

func TestEncodeCursorRefusesTooManyLegs(t *testing.T) {
	m := make(map[int64]int64)
	for i := int64(1); i <= maxCursorLegs+1; i++ {
		m[i] = 1
	}
	if _, err := encodeCursor(m); err == nil {
		t.Fatal("encodeCursor accepted more legs than decodeCursor allows")
	}
}
