// Package hooks is the single place language-specific Go code is wired in.
//
// Adding a language that needs custom behaviour is two steps and touches nothing
// else: create a package under this one, and add a line to All. A language that
// needs no custom behaviour — most of them — needs neither, only a block in
// configs/languages.toml.
package hooks

import (
	"github.com/JustModo/citron/internal/lang"
	"github.com/JustModo/citron/internal/lang/hooks/java"
)

// All returns every language hook, keyed by the `hook` field in languages.toml.
//
// A manifest naming a hook that is missing here fails validation at startup, so a
// forgotten entry is a loud boot error rather than a language that quietly
// misbehaves.
func All() lang.Hooks {
	return lang.Hooks{
		"java": java.Hook{},
	}
}
