package env

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectIncludesOSAndCwd(t *testing.T) {
	dir := t.TempDir()
	info := Collect(dir, 10)
	if info.Directory != dir {
		t.Errorf("directory = %q", info.Directory)
	}
	if info.OS == "" {
		t.Error("os should not be empty")
	}
	if info.Cwd == "" {
		t.Error("cwd should not be empty")
	}
}

func TestFileTreeListsFilesAndHonorsLimit(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(dir, "sub", "f"+string(rune('a'+i))+".txt"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	tree := fileTree(dir, 100)
	if !strings.Contains(tree, "sub") {
		t.Errorf("tree should include sub dir: %q", tree)
	}
	if !strings.Contains(tree, "fa.txt") {
		t.Errorf("tree should include files: %q", tree)
	}

	limited := fileTree(dir, 2)
	if strings.Count(limited, "\n") >= 5 {
		t.Errorf("file tree should be limited: %q", limited)
	}
}

func TestCollectEnvVars(t *testing.T) {
	keys := []string{"PATH"}
	vars := CollectEnvVars(keys)
	if vars["PATH"] == "" {
		t.Error("PATH should be present")
	}
	if _, ok := vars["DOES_NOT_EXIST_XYZ"]; ok {
		t.Error("missing env var should not appear")
	}
}