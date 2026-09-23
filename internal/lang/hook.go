package lang

// Hook supplies language behaviour a manifest template cannot express.
// Implementations must treat the submitted source as hostile input.
type Hook interface {
	// Files derives the source filename and artifact name from the submitted source.
	Files(source []byte, m Manifest) (src, binary string)
}

// Hooks maps a manifest's `hook` field to its implementation.
type Hooks map[string]Hook
