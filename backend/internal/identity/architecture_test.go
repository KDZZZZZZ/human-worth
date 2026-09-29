package identity

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLayerDependencies(t *testing.T) {
	const module = "github.com/KDZZZZZZ/human-worth/backend/"
	const identity = module + "internal/identity/"
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		if !strings.Contains(filepath.ToSlash(path), "/") {
			t.Errorf("production code bypasses layers: %s", path)
		}
		for _, spec := range parsed.Imports {
			dependency, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			// Standard library import paths have no domain in their first component.
			if !strings.Contains(strings.Split(dependency, "/")[0], ".") {
				continue
			}
			allowed := true
			switch {
			case strings.HasPrefix(path, "domain/"), strings.HasPrefix(path, "application/dto/"):
				allowed = false
			case strings.HasPrefix(path, "application/"):
				allowed = dependency == identity+"domain" || dependency == identity+"application/dto"
			case strings.HasPrefix(path, "repo/"), strings.HasPrefix(path, "adapter/"):
				if strings.HasPrefix(dependency, module) {
					allowed = dependency == identity+"domain" || dependency == identity+"application" || dependency == identity+"application/dto"
				}
			case strings.HasPrefix(path, "transport/"):
				allowed = !strings.Contains(dependency, "/repo/") && !strings.Contains(dependency, "/adapter/") && !strings.Contains(dependency, "pgx") && !strings.Contains(dependency, "jwt")
			}
			if !allowed {
				t.Errorf("%s imports forbidden dependency %s", path, dependency)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
