// Package java holds the Java-specific behaviour that a manifest template cannot
// express, and nothing else. Everything configurable about Java — its id, compile and
// run commands, limit multipliers — stays in configs/languages.toml.
package java

import (
	"regexp"

	"github.com/JustModo/judge/internal/lang"
)

// Hook names the source file after the public class it declares. javac requires the
// two to match, so unlike every other supported language the filename cannot be a
// constant in the manifest.
type Hook struct{}

// '$' is a legal Java identifier character but is excluded deliberately: the name
// becomes a filename and an argv element, and no real top-level public class uses it.
var (
	publicClass = regexp.MustCompile(`(?m)^\s*public\s+(?:final\s+|abstract\s+|strictfp\s+)*(?:class|interface|enum|record)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	identifier  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

const maxClassNameLength = 64

func (Hook) Files(source []byte, m lang.Manifest) (string, string) {
	match := publicClass.FindSubmatch(source)
	if match == nil {
		return m.Source, m.Binary
	}
	name := string(match[1])
	// Re-checked even though the pattern already constrains it: this value becomes a
	// path and a command-line argument.
	if !identifier.MatchString(name) || len(name) > maxClassNameLength {
		return m.Source, m.Binary
	}
	return name + ".java", name
}
