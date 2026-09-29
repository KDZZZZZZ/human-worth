package challenge_test

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
	const challenge = module + "internal/challenge/"
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		path = filepath.ToSlash(path)
		if !strings.Contains(path, "/") {
			t.Errorf("production code bypasses layers: %s", path)
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			dependency, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			standard := !strings.Contains(strings.Split(dependency, "/")[0], ".")
			allowed := true
			switch {
			case strings.HasPrefix(path, "domain/"):
				allowed = standard
			case strings.HasPrefix(path, "application/dto/"):
				allowed = standard || dependency == challenge+"domain"
			case strings.HasPrefix(path, "application/"), strings.HasPrefix(path, "agent/"):
				allowed = standard || dependency == challenge+"domain" || dependency == challenge+"application/dto"
				if dependency == "os" || dependency == "os/exec" || dependency == "syscall" || dependency == "database/sql" || dependency == "net" || strings.HasPrefix(dependency, "net/") {
					allowed = false
				}
			case strings.HasPrefix(path, "transport/"):
				allowed = !strings.Contains(dependency, "/repo/") && !strings.Contains(dependency, "pgx") && (!strings.Contains(dependency, "/adapter/") || dependency == challenge+"adapter/protobuf")
			case strings.HasPrefix(path, "adapter/"):
				allowed = !strings.Contains(dependency, "/repo/") && !strings.Contains(dependency, "/transport/") && !strings.Contains(dependency, "pgx")
			case strings.HasPrefix(path, "repo/"):
				allowed = !strings.Contains(dependency, "/transport/") && !strings.Contains(dependency, "/agent") && !strings.HasPrefix(dependency, "google.golang.org/grpc")
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
