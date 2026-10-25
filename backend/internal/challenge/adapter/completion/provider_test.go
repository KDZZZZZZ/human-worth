package completion

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateProjectedConfiguration(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "model.json")
	link := filepath.Join(dir, "projected.json")
	data := []byte(`{"base_url":"https://provider.example","model":"fixture","protocol":"completion","api_key":"fixture-only"}`)
	if e := os.WriteFile(target, data, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(target, link); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []os.FileMode{0600, 0400, 0440, 0640, 0644, 0660, 0610} {
		if e := os.Chmod(target, mode); e != nil {
			t.Fatal(e)
		}
		_, e := LoadProvider(link)
		want := mode == 0600 || mode == 0400 || mode == 0440 || mode == 0640
		if (e == nil) != want {
			t.Errorf("mode %o: allowed=%v", mode, e == nil)
		}
	}
	if _, e := LoadProvider(dir); e == nil {
		t.Fatal("directory accepted")
	}
}
