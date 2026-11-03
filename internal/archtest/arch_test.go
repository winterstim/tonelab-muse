// Package archtest enforces the dependency rules that keep pkg/ portable,
// so other Go programs can import it with their own DAW adapters:
//   - pkg/core imports nothing from this module;
//   - nothing in pkg/ imports internal/ (internal is this app's wiring);
//   - pkg/agent imports only pkg/core: it reaches the DAW, models and
//     measurements through ports, never through an adapter.
package archtest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

const mod = "github.com/winterstim/tonelab-muse/"

func TestDependencyRules(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "web") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		pkg := filepath.ToSlash(filepath.Dir(rel))
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, im := range f.Imports {
			dep, ok := strings.CutPrefix(strings.Trim(im.Path.Value, `"`), mod)
			if !ok {
				continue
			}
			switch {
			case pkg == "pkg/core":
				t.Errorf("%s: pkg/core must not import %s", rel, dep)
			case strings.HasPrefix(pkg, "pkg/") && strings.HasPrefix(dep, "internal/"):
				t.Errorf("%s: pkg/ must not import %s", rel, dep)
			case strings.HasPrefix(pkg, "pkg/agent") && dep != "pkg/core":
				t.Errorf("%s: pkg/agent may import only pkg/core, not %s", rel, dep)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
