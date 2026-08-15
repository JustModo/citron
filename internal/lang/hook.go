package lang

import (
	"regexp"
	"time"
)

func scale(d time.Duration, f float64) time.Duration {
	return time.Duration(float64(d) * f)
}

// Hook is the escape hatch for a language whose behaviour a manifest template cannot
// express. Implementations must treat the source as hostile input.
type Hook interface {
	// Files derives the source filename and artifact name from the submitted source.
	Files(source []byte, m Manifest) (src, binary string)
}

var hooks = map[string]Hook{
	"java": javaHook{},
}

// javac requires a file named after the public class it contains, so the filename
// has to be read out of the source rather than fixed by the manifest.
type javaHook struct{}

// '$' is a legal Java identifier character but is excluded here: the name becomes a
// filename, and no real top-level public class uses it.
var (
	javaPublicClass = regexp.MustCompile(`(?m)^\s*public\s+(?:final\s+|abstract\s+|strictfp\s+)*(?:class|interface|enum|record)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	javaIdentifier  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func (javaHook) Files(source []byte, m Manifest) (string, string) {
	match := javaPublicClass.FindSubmatch(source)
	if match == nil {
		return m.Source, m.Binary
	}
	name := string(match[1])
	// The name becomes a filename and an argv element, so re-check it even though
	// the pattern already constrains it.
	if !javaIdentifier.MatchString(name) || len(name) > 64 {
		return m.Source, m.Binary
	}
	return name + ".java", name
}
