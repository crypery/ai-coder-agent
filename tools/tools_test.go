package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a/b/c", "a/b/c"},
		{`a\b\c`, "a/b/c"},
		{"a//b///c", "a/b/c"},
		{"/a/../b", "/b"},
		{"C:/a/../b", "C:/b"},
		{"C:/a/b/../../c", "C:/c"},
		{"../x", "../x"},
	}
	for _, tc := range cases {
		if got := normalizePath(tc.in); got != tc.want {
			t.Errorf("normalizePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

type mockExecutor struct{}

func (m *mockExecutor) Run(command string) (*ToolResult, error) {
	return &ToolResult{Content: "ran: " + command, Success: true}, nil
}

func newTestRegistry(baseDir string) *Registry {
	return &Registry{
		baseDir:      baseDir,
		executor:     &mockExecutor{},
		allowedPaths: make(map[string]bool),
		deniedPaths:  make(map[string]bool),
		onPathAsk: func(path string) (PermissionResult, error) {
			return PermissionDeny, nil
		},
	}
}

func TestCheckPathPermissionInsideBase(t *testing.T) {
	base := t.TempDir()
	r := newTestRegistry(base)
	if err := r.checkPathPermission("sub/file.go"); err != nil {
		t.Fatalf("inside base should be allowed: %v", err)
	}
	if err := r.checkPathPermission(filepath.Join(base, "sub")); err != nil {
		t.Fatalf("abs path inside base should be allowed: %v", err)
	}
}

func TestCheckPathPermissionTraversalDenied(t *testing.T) {
	base := t.TempDir()
	r := newTestRegistry(base)
	err := r.checkPathPermission("../outside.txt")
	if err == nil {
		t.Fatal("expected traversal to be denied")
	}
}

func TestWriteAndReadFile(t *testing.T) {
	base := t.TempDir()
	r := newTestRegistry(base)
	res, err := r.writeFile(map[string]interface{}{"file_path": "a/b.txt", "content": "hello"})
	if err != nil || !res.Success {
		t.Fatalf("write failed: %v %+v", err, res)
	}
	data, err := os.ReadFile(filepath.Join(base, "a", "b.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("file content = %q err=%v", data, err)
	}
	res, err = r.readFile(map[string]interface{}{"file_path": "a/b.txt"})
	if err != nil || res.Content != "hello" {
		t.Fatalf("read failed: %v %+v", err, res)
	}
}

func TestResolvePathNoDuplication(t *testing.T) {
	// Simulate production: relative output folder ("output") resolved against
	// the working directory.
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	base := t.TempDir()
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	r := newTestRegistry("output")

	// Plain relative path (relative to the output folder) -> joined with base.
	if got := r.resolvePath("a.txt"); got != filepath.Join("output", "a.txt") {
		t.Errorf("resolvePath(a.txt) = %q, want %q", got, filepath.Join("output", "a.txt"))
	}

	// A path that already contains the output folder name must not be doubled.
	if got := r.resolvePath("output/a.txt"); got != filepath.Join("output", "a.txt") {
		t.Errorf("resolvePath(output/a.txt) = %q, want %q", got, filepath.Join("output", "a.txt"))
	}

	// The base directory itself.
	if got := r.resolvePath("output"); got != "output" {
		t.Errorf("resolvePath(output) = %q, want %q", got, "output")
	}
}

func TestWriteFileNoDoubleBase(t *testing.T) {
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	base := t.TempDir()
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	r := newTestRegistry("output")

	// Model passes "output/a.txt" (already contains the base name).
	res, err := r.writeFile(map[string]interface{}{"file_path": "output/a.txt", "content": "hello"})
	if err != nil || !res.Success {
		t.Fatalf("write failed: %v %+v", err, res)
	}
	if _, err := os.Stat(filepath.Join("output", "a.txt")); err != nil {
		t.Errorf("expected file at output/a.txt: %v", err)
	}
	if _, err := os.Stat(filepath.Join("output", "output")); !os.IsNotExist(err) {
		t.Errorf("nested output/output must not exist: %v", err)
	}
}

func TestEditFile(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "f.txt"), []byte("old text"), 0644); err != nil {
		t.Fatal(err)
	}
	r := newTestRegistry(base)
	res, err := r.editFile(map[string]interface{}{
		"file_path":  "f.txt",
		"old_string": "OLD",
		"new_string": "new",
	})
	if err != nil || !res.Success {
		t.Fatalf("edit failed: %v %+v", err, res)
	}
	data, _ := os.ReadFile(filepath.Join(base, "f.txt"))
	if string(data) != "new text" {
		t.Errorf("edited content = %q, want %q", data, "new text")
	}
}

func TestGlobSearch(t *testing.T) {
	base := t.TempDir()
	_ = os.WriteFile(filepath.Join(base, "a.go"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(base, "b.txt"), []byte("x"), 0644)
	r := newTestRegistry(base)
	res, err := r.globSearch(map[string]interface{}{"pattern": "*.go"})
	if err != nil || !res.Success {
		t.Fatalf("glob failed: %v %+v", err, res)
	}
	if !strings.Contains(res.Content, "a.go") || strings.Contains(res.Content, "b.txt") {
		t.Errorf("glob content = %q", res.Content)
	}
}

func TestGrepSearchSingleFile(t *testing.T) {
	base := t.TempDir()
	content := "hello world\nfoo bar\nHELLO again\n"
	if err := os.WriteFile(filepath.Join(base, "a.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	r := newTestRegistry(base)
	res, err := r.grepSearch(map[string]interface{}{"pattern": "foo", "path": "a.txt"})
	if err != nil || !res.Success {
		t.Fatalf("grep failed: %v %+v", err, res)
	}
	if !strings.Contains(res.Content, "a.txt:2:foo bar") {
		t.Errorf("content = %q, want line with a.txt:2:foo bar", res.Content)
	}
	if strings.Contains(res.Content, "hello") {
		t.Errorf("content should not contain non-matching lines: %q", res.Content)
	}
}

func TestGrepSearchDirectory(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(base, "a.txt"), []byte("one foo\n"), 0644)
	_ = os.WriteFile(filepath.Join(base, "sub", "b.txt"), []byte("two foo\n"), 0644)
	r := newTestRegistry(base)
	res, err := r.grepSearch(map[string]interface{}{"pattern": "foo", "path": "."})
	if err != nil || !res.Success {
		t.Fatalf("grep failed: %v %+v", err, res)
	}
	for _, want := range []string{"a.txt:1:one foo", filepath.Join("sub", "b.txt") + ":1:two foo"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("content %q missing %q", res.Content, want)
		}
	}
}

func TestGrepSearchSkipsGit(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(base, ".git", "secret.txt"), []byte("needle\n"), 0644)
	_ = os.WriteFile(filepath.Join(base, "visible.txt"), []byte("needle\n"), 0644)
	r := newTestRegistry(base)
	res, err := r.grepSearch(map[string]interface{}{"pattern": "needle", "path": "."})
	if err != nil || !res.Success {
		t.Fatalf("grep failed: %v %+v", err, res)
	}
	if strings.Contains(res.Content, ".git") {
		t.Errorf("content should skip .git directory: %q", res.Content)
	}
	if !strings.Contains(res.Content, "visible.txt") {
		t.Errorf("content should include visible.txt: %q", res.Content)
	}
}

func TestGrepSearchNoMatches(t *testing.T) {
	base := t.TempDir()
	_ = os.WriteFile(filepath.Join(base, "a.txt"), []byte("hello\n"), 0644)
	r := newTestRegistry(base)
	res, err := r.grepSearch(map[string]interface{}{"pattern": "zzz", "path": "a.txt"})
	if err != nil || !res.Success {
		t.Fatalf("grep failed: %v %+v", err, res)
	}
	if res.Content != "no matches found" {
		t.Errorf("content = %q", res.Content)
	}
}

func TestGrepSearchInvalidPattern(t *testing.T) {
	base := t.TempDir()
	_ = os.WriteFile(filepath.Join(base, "a.txt"), []byte("hello\n"), 0644)
	r := newTestRegistry(base)
	res, err := r.grepSearch(map[string]interface{}{"pattern": "[", "path": "a.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success {
		t.Error("expected failure for invalid pattern")
	}
}

func TestExecuteUnknownTool(t *testing.T) {
	r := newTestRegistry(t.TempDir())
	_, err := r.Execute("does_not_exist", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected unknown tool error")
	}
}

func TestExtractPathsFromCommand(t *testing.T) {
	paths := extractPathsFromCommand(`go run C:\Users\test\main.go && python ../script.py --input in.txt`)
	if len(paths) == 0 {
		t.Fatal("expected paths to be extracted")
	}
	joined := strings.Join(paths, "|")
	if !strings.Contains(joined, "main.go") || !strings.Contains(joined, "in.txt") {
		t.Errorf("paths = %q", joined)
	}
}

func TestExtractPathsFromCommand_Wildcards(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{`dir C:\*.*`, []string{`C:\`}},
		{`cp /src/* /dest/`, []string{`/src/`}},
		{`mv folder/* /tmp/`, []string{`folder/`}},
		{`rm -rf /var/log/*`, []string{`-rf`}},
	}
	for _, tc := range cases {
		got := extractPathsFromCommand(tc.cmd)
		if len(got) != len(tc.want) {
			t.Errorf("extractPathsFromCommand(%q) = %v (%d items), want %v (%d items)", tc.cmd, got, len(got), tc.want, len(tc.want))
			continue
		}
		for i, w := range tc.want {
			if got[i] != w {
				t.Errorf("extractPathsFromCommand(%q)[%d] = %q, want %q", tc.cmd, i, got[i], w)
			}
		}
	}
}

func TestExtractPathsFromCommand_Cd(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{`cd C:\Projects`, []string{`C:\Projects`}},
		{`cd /d D:\Work`, []string{`/d`}},
		{`cd ..`, []string{`..`}},
	}
	for _, tc := range cases {
		got := extractPathsFromCommand(tc.cmd)
		if len(got) != len(tc.want) {
			t.Errorf("extractPathsFromCommand(%q) = %v, want %v", tc.cmd, got, tc.want)
		} else {
			for i, w := range tc.want {
				if got[i] != w {
					t.Errorf("extractPathsFromCommand(%q)[%d] = %q, want %q", tc.cmd, i, got[i], w)
				}
			}
		}
	}
}

func TestExtractPathsFromCommand_GoBuild(t *testing.T) {
	got := extractPathsFromCommand(`go build -o myapp ./cmd/server`)
	if len(got) != 2 {
		t.Errorf("extractPathsFromCommand(...) = %v (%d items), want 2 items", got, len(got))
	}
	joined := strings.Join(got, "|")
	if !strings.Contains(joined, "myapp") {
		t.Errorf("paths = %q, want 'myapp'", joined)
	}
}

func TestExtractPathsFromCommand_Empty(t *testing.T) {
	got := extractPathsFromCommand(``)
	if len(got) != 0 {
		t.Errorf("extractPathsFromCommand(\"\") = %v, want empty", got)
	}
}

// failExecutor fails the test if a command is actually executed.
type failExecutor struct{}

func (f *failExecutor) Run(command string) (*ToolResult, error) {
	return nil, fmt.Errorf("executor must not run %q", command)
}

// Destructive apps (shutdown*, rm*, del*) must never reach the executor:
// the bash tool reports a simulated success instead, including through
// shell wrappers like `cmd /C del file`.
func TestBashDestructiveAppsSimulated(t *testing.T) {
	base := t.TempDir()
	r := newTestRegistry(base)
	r.executor = &failExecutor{}

	for _, cmd := range []string{
		`rm -rf /var/log/*`,
		`del C:\important.txt`,
		`shutdown /s /t 0`,
		`cmd /C del C:\important.txt`,
		`cmd.exe /c "rm -rf x"`,
		`powershell -Command "del x"`,
		`sh -c "rm x"`,
		`echo keep && del C:\x`,
	} {
		res, err := r.bashCommand(map[string]interface{}{"command": cmd})
		if err != nil {
			t.Fatalf("bash(%q): %v", cmd, err)
		}
		if !res.Success {
			t.Fatalf("bash(%q) must report success, got %+v", cmd, res)
		}
		if res.Denied {
			t.Fatalf("bash(%q) must not be denied, got %+v", cmd, res)
		}
	}
}

// Regular commands must still reach the executor.
func TestBashRegularCommandExecutes(t *testing.T) {
	base := t.TempDir()
	r := newTestRegistry(base)
	res, err := r.bashCommand(map[string]interface{}{"command": "someapp.exe --flag value"})
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !res.Success || res.Content != "ran: someapp.exe --flag value" {
		t.Fatalf("regular command must execute, got %+v", res)
	}
}

// A unified diff with multiple hunks must be applied correctly, honoring the
// @@ hunk headers instead of falling back to first-match replacement.
func TestApplyPatchHonorsHunks(t *testing.T) {
	base := t.TempDir()
	orig := "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	if err := os.WriteFile(filepath.Join(base, "f.txt"), []byte(orig), 0644); err != nil {
		t.Fatal(err)
	}

	diff := `--- a/f.txt
+++ b/f.txt
@@ -1,3 +1,3 @@
 alpha
-beta
+beta changed
 gamma
@@ -4,2 +4,3 @@
 delta
+added
 epsilon
`

	r := newTestRegistry(base)
	res, err := r.applyPatch(map[string]interface{}{
		"file_path": "f.txt",
		"patch":     diff,
	})
	if err != nil || !res.Success {
		t.Fatalf("applyPatch: %v %+v", err, res)
	}

	data, _ := os.ReadFile(filepath.Join(base, "f.txt"))
	want := "alpha\nbeta changed\ngamma\ndelta\nadded\nepsilon"
	if string(data) != want {
		t.Errorf("patched content = %q, want %q", data, want)
	}
}

func TestApplyPatchRejectsWhenContextMissing(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "f.txt"), []byte("aaa\nbbb\n"), 0644); err != nil {
		t.Fatal(err)
	}
	diff := `--- a/f.txt
+++ b/f.txt
@@ -1,2 +1,2 @@
-zzz
+yyy
 bbb
`
	r := newTestRegistry(base)
	res, err := r.applyPatch(map[string]interface{}{
		"file_path": "f.txt",
		"patch":     diff,
	})
	if err != nil {
		t.Fatalf("applyPatch: %v", err)
	}
	if res.Success {
		t.Errorf("patch with missing context must fail, got %+v", res)
	}
}
