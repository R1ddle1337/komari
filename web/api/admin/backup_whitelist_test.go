package admin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupIncludesPersistentPluginData(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	for _, root := range []string{"plugin-data", "plguin-data"} {
		path := filepath.Join(source, root, "provider", "state.json")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"configured":true}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyWhitelistedFilesFrom(source, target); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"plugin-data", "plguin-data"} {
		got, err := os.ReadFile(filepath.Join(target, root, "provider", "state.json"))
		if err != nil || string(got) != `{"configured":true}` {
			t.Fatalf("persistent %s missing or changed: %q, %v", root, got, err)
		}
	}
}
