package compare

import "testing"

func TestTrimTrailing(t *testing.T) {
	tests := []struct {
		name             string
		expected, actual string
		want             bool
	}{
		{"identical", "5\n", "5\n", true},
		{"missing trailing newline", "5\n", "5", true},
		{"extra trailing newlines", "5", "5\n\n\n", true},
		{"trailing spaces", "5", "5   ", true},
		{"windows line endings", "a\nb\n", "a\r\nb\r\n", true},
		{"lone carriage returns", "a\nb", "a\rb", true},
		{"both empty", "", "", true},
		{"empty vs whitespace", "", "  \n\t", true},
		{"different values", "5", "6", false},
		{"leading whitespace matters", "5", " 5", false},
		{"interior whitespace matters", "a b", "a  b", false},
		{"missing line", "a\nb", "a", false},
		{"empty vs value", "", "5", false},
		{"unicode", "héllo", "héllo", true},
		{"unicode differs", "héllo", "hello", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (TrimTrailing{}).Equal([]byte(tt.expected), []byte(tt.actual)); got != tt.want {
				t.Errorf("Equal(%q, %q) = %v, want %v", tt.expected, tt.actual, got, tt.want)
			}
		})
	}
}

func TestExactIsStricter(t *testing.T) {
	if (Exact{}).Equal([]byte("5"), []byte("5\n")) {
		t.Error("Exact should not ignore a trailing newline")
	}
	if !(Exact{}).Equal([]byte("5\n"), []byte("5\n")) {
		t.Error("Exact should match identical bytes")
	}
}

// Comparison must not mutate what it is given; results are reported to the client
// after the verdict is decided.
func TestInputsAreNotMutated(t *testing.T) {
	expected := []byte("value\n\n")
	actual := []byte("value  ")
	(TrimTrailing{}).Equal(expected, actual)
	if string(expected) != "value\n\n" || string(actual) != "value  " {
		t.Errorf("comparator mutated its inputs: %q %q", expected, actual)
	}
}

func TestDefaultIsTrimTrailing(t *testing.T) {
	if Default().Name() != (TrimTrailing{}).Name() {
		t.Errorf("default comparator is %q; the consumer relies on trailing-whitespace tolerance", Default().Name())
	}
}
