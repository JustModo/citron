package compare

import "bytes"

// Comparator is an output-matching policy.
type Comparator interface {
	Equal(expected, actual []byte) bool
	Name() string
}

// TrimTrailing compares output ignoring only trailing whitespace and line-ending
// style, since programs commonly differ in final newlines or CRLF output.
type TrimTrailing struct{}

// Name returns "trim-trailing".
func (TrimTrailing) Name() string { return "trim-trailing" }

// Equal reports whether expected and actual match after normalization.
func (TrimTrailing) Equal(expected, actual []byte) bool {
	return bytes.Equal(normalize(expected), normalize(actual))
}

// normalize converts CRLF and CR to LF and trims trailing whitespace. It copies
// before rewriting, so b is never modified.
func normalize(b []byte) []byte {
	if bytes.IndexByte(b, '\r') >= 0 {
		b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
		b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	}
	return bytes.TrimRight(b, " \t\n\v\f")
}

// Default returns the comparator used when a submission does not specify one.
func Default() Comparator { return TrimTrailing{} }
