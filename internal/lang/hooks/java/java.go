package java

import (
	"regexp"

	"github.com/JustModo/citron/internal/lang"
)

// Hook names the source file after the public class, as javac requires, and the
// run target after the class declaring main, which may be a different,
// package-private class.
type Hook struct{}

// '$' is legal in Java identifiers but excluded: names become a filename and argv.
var (
	publicType = regexp.MustCompile(`(?m)^\s*public\s+(?:final\s+|abstract\s+|strictfp\s+)*(?:class|interface|enum|record)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	anyType    = regexp.MustCompile(`(?m)^\s*(?:public\s+|final\s+|abstract\s+|strictfp\s+)*(?:class|enum|record)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	mainMethod = regexp.MustCompile(`public\s+static\s+(?:final\s+)?void\s+main\s*\(`)
	identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

const maxClassNameLength = 64

// Files returns the source filename and class to run, falling back to the
// manifest's names when none can be derived.
func (Hook) Files(source []byte, m lang.Manifest) (sourceFile, binary string) {
	sourceFile, binary = m.Source, m.Binary

	if name, ok := valid(publicType.FindSubmatch(source)); ok {
		sourceFile, binary = name+".java", name
	}
	if name, ok := valid(entrypointClass(source)); ok {
		binary = name
	}
	return sourceFile, binary
}

// entrypointClass returns the submatch for the last type declared before the
// first main method, taken as the class containing it.
func entrypointClass(source []byte) [][]byte {
	main := mainMethod.FindIndex(source)
	if main == nil {
		return nil
	}
	var last [][]byte
	for _, m := range anyType.FindAllSubmatchIndex(source[:main[0]], -1) {
		last = [][]byte{source[m[0]:m[1]], source[m[2]:m[3]]}
	}
	return last
}

func valid(match [][]byte) (string, bool) {
	if match == nil {
		return "", false
	}
	name := string(match[1])
	// Re-checked despite the patterns: the name becomes a path and an argv element.
	if !identifier.MatchString(name) || len(name) > maxClassNameLength {
		return "", false
	}
	return name, true
}
