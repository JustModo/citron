package hooks

import (
	"github.com/JustModo/citron/internal/lang"
	"github.com/JustModo/citron/internal/lang/hooks/java"
)

// All returns every language hook, keyed by the `hook` field in languages.toml.
// A manifest naming a hook missing here fails validation at startup.
func All() lang.Hooks {
	return lang.Hooks{
		"java": java.Hook{},
	}
}
