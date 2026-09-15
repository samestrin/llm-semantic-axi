package output

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// exactifyNumbers decides, per number, whether --axi may hand the value to the
// encoder as a number or must carry its digits across as text. The boundary is
// 2^53 and it is asymmetric (`> maxExactInt`, not `>=`), so both sides of it
// are pinned here rather than inferred from one example.

func axiOf(t *testing.T, v any) string {
	t.Helper()
	buf := new(bytes.Buffer)
	f := New(false, false, buf)
	f.AXI = true
	if err := f.Print(v, nil); err != nil {
		t.Fatalf("Print: %v", err)
	}
	return buf.String()
}

func TestAXIIntegerBoundaryAtTwoToThe53(t *testing.T) {
	// 2^53 is the largest integer float64 holds exactly, so it is the last value
	// that may stay numeric. 2^53+1 is the first that cannot: float64 rounds it
	// back down to 2^53, which is how --json and --axi came to disagree about
	// the same byte count. The negative side must behave identically, or a
	// signed total changes meaning when it crosses zero.
	for _, tc := range []struct {
		name   string
		in     int64
		digits string
		quoted bool // past the boundary the value is carried across as text
	}{
		{"just below 2^53", 1<<53 - 1, "9007199254740991", false},
		{"exactly 2^53", 1 << 53, "9007199254740992", false},
		{"just above 2^53", 1<<53 + 1, "9007199254740993", true},
		{"well above 2^53", 1<<62 + 1, "4611686018427387905", true},
		{"exactly -2^53", -(1 << 53), "-9007199254740992", false},
		{"just below -2^53", -(1<<53 + 1), "-9007199254740993", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := "n: " + tc.digits
			if tc.quoted {
				want = `n: "` + tc.digits + `"`
			}
			got := axiOf(t, map[string]int64{"n": tc.in})
			if !strings.Contains(got, want) {
				t.Errorf("--axi emitted %q, want it to contain %q", strings.TrimSpace(got), want)
			}
			// An in-range value must not be quoted, or --axi stops matching
			// --json for every ordinary payload.
			if !tc.quoted && strings.Contains(got, `"`+tc.digits+`"`) {
				t.Errorf("an in-range integer was stringified: %q", strings.TrimSpace(got))
			}
			// Whichever side of the boundary, the digits must match --json
			// exactly. That agreement is the whole point of the projection.
			jsonBuf := new(bytes.Buffer)
			if err := New(true, false, jsonBuf).Print(map[string]int64{"n": tc.in}, nil); err != nil {
				t.Fatalf("Print: %v", err)
			}
			if !strings.Contains(jsonBuf.String(), tc.digits) {
				t.Errorf("--json and --axi disagree: --json says %q, --axi says %q",
					strings.TrimSpace(jsonBuf.String()), tc.digits)
			}
		})
	}
}

func TestAXIUnsignedBoundaryAtTwoToThe53(t *testing.T) {
	// A uint64 past 2^63 does not fit ParseInt at all, so it takes the second
	// branch. It must land on the same rule as the signed side.
	got := axiOf(t, map[string]uint64{"n": 1 << 53})
	if !strings.Contains(got, "n: 9007199254740992") {
		t.Errorf("uint64 at exactly 2^53 was not left numeric: %q", strings.TrimSpace(got))
	}

	got = axiOf(t, map[string]uint64{"n": 18446744073709551615})
	if !strings.Contains(got, "18446744073709551615") {
		t.Errorf("uint64 max lost its digits: %q", strings.TrimSpace(got))
	}
}

func TestAXIKeepsFractionalNumbersNumeric(t *testing.T) {
	// A float is never an exact-integer problem, so it must take the float
	// branch and stay a number. Stringifying scores would break every consumer
	// that compares a similarity against a threshold.
	got := axiOf(t, map[string]float64{"score": 0.8125})
	if !strings.Contains(got, "score: 0.8125") {
		t.Errorf("a float was not left numeric: %q", strings.TrimSpace(got))
	}
	if strings.Contains(got, `"0.8125"`) {
		t.Errorf("a float was quoted: %q", strings.TrimSpace(got))
	}
}

func TestAXICarriesDigitsThatAreNotRepresentableAtAll(t *testing.T) {
	// A literal past the float64 range fails ParseInt, ParseUint and Float64.
	// There is no numeric form left, so the verbatim digits are all the encoder
	// can be given — dropping to +Inf or to 0 would silently corrupt the value.
	got := axiOf(t, map[string]json.RawMessage{"n": json.RawMessage("1e999")})
	if !strings.Contains(got, "1e999") {
		t.Errorf("an out-of-range literal was not carried across verbatim: %q", strings.TrimSpace(got))
	}
}

func TestAXIExactifiesInsideNestedContainers(t *testing.T) {
	// The rule has to reach every number in the document, not just the top
	// level: search results carry their sizes inside a row array.
	type row struct {
		File  string `json:"file"`
		Bytes int64  `json:"bytes"`
	}
	got := axiOf(t, map[string]any{
		"rows":  []row{{File: "a.bin", Bytes: 1<<53 + 7}},
		"inner": map[string]any{"deep": int64(1<<53 + 9)},
	})
	for _, want := range []string{"9007199254740999", "9007199254741001"} {
		if !strings.Contains(got, want) {
			t.Errorf("nested value lost its digits: %q missing from %q", want, strings.TrimSpace(got))
		}
	}
}

// --- remaining formatter branches ---

func TestPrint_FallsBackToJSONWhenNoTextFunctionIsGiven(t *testing.T) {
	// Default text mode with a nil textFunc is what a command that has not
	// written a renderer yet passes. Printing nothing would look like an empty
	// result set, so JSON is the fallback.
	var buf bytes.Buffer
	if err := New(false, false, &buf).Print(TestOutput{Name: "fallback"}, nil); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if !strings.Contains(buf.String(), `"name": "fallback"`) {
		t.Errorf("no JSON fallback was emitted: %q", buf.String())
	}
}

func TestMinimalJSON_TagWithNoNameKeepsTheGoFieldName(t *testing.T) {
	// `json:",omitempty"` sets an option without renaming the field. The name
	// must come from the Go identifier, not from the empty string before the
	// comma, or the key disappears from the document.
	type tagged struct {
		Message string `json:",omitempty"`
		Blank   string `json:",omitempty"`
	}
	var buf bytes.Buffer
	if err := New(true, true, &buf).Print(tagged{Message: "hi"}, nil); err != nil {
		t.Fatalf("Print: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Invalid JSON %q: %v", buf.String(), err)
	}
	// "Message" lowercases to "message", which does have an abbreviation.
	if parsed["msg"] != "hi" {
		t.Errorf(`expected key "msg" = "hi", got %v`, parsed)
	}
	if _, ok := parsed["Blank"]; ok {
		t.Errorf("omitempty was not honoured for an unnamed tag: %v", parsed)
	}
}

func TestMinimalJSON_SkipsUnexportedFields(t *testing.T) {
	// reflect cannot call Interface() on an unexported field, so processStruct
	// has to skip it. Without that guard the formatter panics on any result type
	// that keeps private state — and panicking is how a command turns a
	// successful search into a stack trace.
	type withPrivate struct {
		Name   string `json:"name"`
		secret string //nolint:unused // presence is the point
	}
	var buf bytes.Buffer
	if err := New(true, true, &buf).Print(withPrivate{Name: "visible", secret: "hidden"}, nil); err != nil {
		t.Fatalf("Print: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("Invalid JSON %q: %v", buf.String(), err)
	}
	if parsed["n"] != "visible" {
		t.Errorf(`expected key "n" = "visible", got %v`, parsed)
	}
	if len(parsed) != 1 {
		t.Errorf("an unexported field reached the document: %v", parsed)
	}
}

func TestIsEmpty_TreatsAnInvalidValueAsEmpty(t *testing.T) {
	// A zero reflect.Value reaches isEmpty from a nil interface element. It has
	// no Kind to switch on, so the guard has to answer before the switch rather
	// than panic mid-render.
	f := New(true, true, new(bytes.Buffer))
	if !f.isEmpty(reflect.Value{}) {
		t.Error("an invalid reflect.Value was not treated as empty")
	}
}

func TestRelativePath_KeepsThePathWhenItCannotBeRelativized(t *testing.T) {
	// filepath.Rel fails outright when one side is absolute and the other is
	// not. There is no correct relative answer, so the original path is returned
	// unchanged rather than a wrong one being invented.
	abs := filepath.Join(string(filepath.Separator), "var", "log", "x.log")
	if got := RelativePath(abs, filepath.Join("relative", "base")); got != abs {
		t.Errorf("RelativePath = %q, want the original %q", got, abs)
	}
}
