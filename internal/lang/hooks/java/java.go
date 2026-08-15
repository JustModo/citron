// Package java holds the Java-specific behaviour that a manifest template cannot
// express, and nothing else. Everything configurable about Java — its id, compile and
// run commands, limit multipliers — stays in configs/languages.toml.
package java

import (
	"regexp"

	"github.com/JustModo/citron/internal/lang"
)

// Hook works out two names Java needs that no other supported language does.
//
// The source filename must match the public class, because javac refuses to compile
// otherwise. The class to *run*, though, is whichever one declares a main method, and
// those are not always the same type: a submission wrapped by a test harness commonly
// has a package-private entrypoint alongside the candidate's public class. Getting
// this wrong compiles cleanly and then fails at run time with "Main method not found".
type Hook struct{}

// '$' is a legal Java identifier character but is excluded deliberately: these names
// become a filename and an argv element, and no real top-level class uses it.
var (
	publicType = regexp.MustCompile(`(?m)^\s*public\s+(?:final\s+|abstract\s+|strictfp\s+)*(?:class|interface|enum|record)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	anyType    = regexp.MustCompile(`(?m)^\s*(?:public\s+|final\s+|abstract\s+|strictfp\s+)*(?:class|enum|record)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	mainMethod = regexp.MustCompile(`public\s+static\s+(?:final\s+)?void\s+main\s*\(`)
	identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

const maxClassNameLength = 64

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

// entrypointClass returns the innermost type declaration preceding a main method,
// which is the class `java` must be pointed at.
func entrypointClass(source []byte) [][]byte {
	main := mainMethod.FindIndex(source)
	if main == nil {
		return nil
	}
	// The last type declared before main is the one that contains it.
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
	// Re-checked even though the patterns already constrain it: these values become a
	// path and a command-line argument.
	if !identifier.MatchString(name) || len(name) > maxClassNameLength {
		return "", false
	}
	return name, true
}
