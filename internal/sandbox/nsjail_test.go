package sandbox

import (
	"slices"
	"testing"
	"time"

	"github.com/JustModo/citron/internal/judge"
)

func TestUserNamespaceIsOnByDefault(t *testing.T) {
	spec := Spec{
		Dir: "/tmp/ws", Argv: []string{"/bin/true"},
		Limits: judge.Limits{WallTime: time.Second, Stack: 8 << 20, MaxFileSize: 1 << 20},
	}
	for _, tt := range []struct {
		cfg  NsjailConfig
		want bool
	}{
		{NsjailConfig{}, false},
		{NsjailConfig{NoUserNamespace: true}, true},
	} {
		args, err := (&Nsjail{cfg: tt.cfg}).args(spec)
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(args, "--disable_clone_newuser"); got != tt.want {
			t.Errorf("NoUserNamespace=%v: --disable_clone_newuser present = %v", tt.cfg.NoUserNamespace, got)
		}
	}
}
