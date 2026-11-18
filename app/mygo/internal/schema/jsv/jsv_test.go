package jsv

import (
	"math"
	"testing"
)

func TestCoercions(t *testing.T) {
	numbers := []struct {
		in   any
		want float64
	}{{"", 0}, {" 12 ", 12}, {"0x10", 16}, {"1e3", 1000}, {true, 1}, {nil, 0}, {[]any{"5"}, 5}, {[]any{}, 0}}
	for _, c := range numbers {
		if got := Number(c.in); got != c.want {
			t.Errorf("Number(%#v) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, in := range []any{"abc", "1 2", []any{1.0, 2.0}, map[string]any{}} {
		if !math.IsNaN(Number(in)) {
			t.Errorf("Number(%#v) not NaN", in)
		}
	}
	strings := []struct {
		in   any
		want string
	}{{1.5, "1.5"}, {1e21, "1e+21"}, {1e-7, "1e-7"}, {100.0, "100"}, {[]any{"a", 1.0, nil}, "a,1,"}, {false, "false"}, {nil, "null"}}
	for _, c := range strings {
		if got := String(c.in); got != c.want {
			t.Errorf("String(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
	if ParseInt(" 42px") != 42 || !math.IsNaN(ParseInt("px")) {
		t.Error("ParseInt")
	}
	if !StrictEqual(1, 1.0) || StrictEqual("1", 1.0) || StrictEqual(map[string]any{}, map[string]any{}) {
		t.Error("StrictEqual")
	}
	if Truthy("") || Truthy(0.0) || !Truthy([]any{}) || Truthy(math.NaN()) {
		t.Error("Truthy")
	}
}
