// Package compare decides whether a program's output matches what was expected.
//
// It is deliberately separate from execution: the sandbox reports what a program
// printed, and nothing about how that is judged leaks into it. Adding a policy
// (float tolerance, token order, a checker program) means adding a Comparator.
package compare

import "bytes"

type Comparator interface {
	Equal(expected, actual []byte) bool
	Name() string
}

// TrimTrailing ignores trailing whitespace and line-ending style, and nothing else.
// This matches the semantics the existing consumer already relies on, so verdicts do
// not change when the judge behind it does.
type TrimTrailing struct{}

func (TrimTrailing) Name() string { return "trim-trailing" }

func (TrimTrailing) Equal(expected, actual []byte) bool {
	return bytes.Equal(normalize(expected), normalize(actual))
}

// Exact compares byte for byte.
type Exact struct{}

func (Exact) Name() string                       { return "exact" }
func (Exact) Equal(expected, actual []byte) bool { return bytes.Equal(expected, actual) }

func normalize(b []byte) []byte {
	if bytes.IndexByte(b, '\r') >= 0 {
		b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
		b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	}
	return bytes.TrimRight(b, " \t\n\v\f")
}

// Default is the comparator used when a submission does not ask for another.
func Default() Comparator { return TrimTrailing{} }
