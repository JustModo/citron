package lang

import "time"

func scale(d time.Duration, f float64) time.Duration {
	return time.Duration(float64(d) * f)
}

// Hook is the escape hatch for a language whose behaviour a manifest template cannot
// express. Implementations live in their own package under lang/hooks and must treat
// the submitted source as hostile input.
type Hook interface {
	// Files derives the source filename and artifact name from the submitted source.
	Files(source []byte, m Manifest) (src, binary string)
}

// Hooks maps a manifest's `hook` field to its implementation. It is passed in from
// the composition root rather than kept in a package-level variable, so nothing
// registers itself behind the caller's back.
type Hooks map[string]Hook
