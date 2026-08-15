package java

import (
	"regexp"
	"strings"
	"testing"

	"github.com/JustModo/citron/internal/lang"
)

var manifest = lang.Manifest{Source: "Main.java", Binary: "Main"}

func TestClassNameExtraction(t *testing.T) {
	tests := []struct {
		name, source, wantSource string
	}{
		{"public class", "public class Foo {}", "Foo.java"},
		{"final", "public final class Bar {}", "Bar.java"},
		{"abstract", "public abstract class Abs {}", "Abs.java"},
		{"record", "public record Point(int x) {}", "Point.java"},
		{"interface", "public interface Shape {}", "Shape.java"},
		{"enum", "public enum Color {}", "Color.java"},
		{"leading imports", "import java.util.*;\n\npublic class Baz {}", "Baz.java"},
		{"indented", "   public class Indented {}", "Indented.java"},
		{"no public class falls back", "class Helper {}", "Main.java"},
		{"keyword only in a comment", "// public class Sneaky\nclass X {}", "Main.java"},
		{"empty source falls back", "", "Main.java"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source, binary := Hook{}.Files([]byte(tt.source), manifest)
			if source != tt.wantSource {
				t.Errorf("source = %q, want %q", source, tt.wantSource)
			}
			if binary+".java" != source {
				t.Errorf("binary %q is inconsistent with source %q", binary, source)
			}
		})
	}
}

// The class name becomes a filename and a command-line argument, so a hostile one
// must never escape the shape of an identifier.
func TestClassNameIsSanitized(t *testing.T) {
	safe := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\.java$`)
	for _, source := range []string{
		"public class ../../etc/passwd {}",
		"public class Foo;rm -rf / {}",
		"public class $(id) {}",
		"public class `whoami` {}",
		"public class " + strings.Repeat("A", 200) + " {}",
	} {
		got, binary := Hook{}.Files([]byte(source), manifest)
		if !safe.MatchString(got) {
			t.Errorf("unsanitized filename from %q: %q", source, got)
		}
		if strings.ContainsAny(binary, "/;$ .`") {
			t.Errorf("unsanitized binary name from %q: %q", source, binary)
		}
	}
}

func TestOverlongClassNameFallsBack(t *testing.T) {
	source := "public class " + strings.Repeat("A", maxClassNameLength+1) + " {}"
	if got, _ := (Hook{}).Files([]byte(source), manifest); got != "Main.java" {
		t.Errorf("got %q, want the fallback Main.java", got)
	}
}
