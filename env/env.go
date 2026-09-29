package env

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Info holds runtime environment details.
type Info struct {
	Directory string
	Cwd       string
	FileTree  string
	EnvVars   map[string]string
	OS        string
	Arch      string
}

// Collect gathers environment information for the given project directory.
func Collect(projectDir string, maxFiles int) *Info {
	i := &Info{
		Directory: projectDir,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}

	cwd, err := os.Getwd()
	if err == nil {
		i.Cwd = cwd
	}

	i.FileTree = fileTree(projectDir, maxFiles)

	return i
}

func fileTree(dir string, maxFiles int) string {
	if maxFiles <= 0 {
		maxFiles = 100
	}
	var lines []string
	count := 0
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if count >= maxFiles {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		lines = append(lines, rel)
		count++
		return nil
	})
	return strings.Join(lines, "\n")
}

// CollectEnvVars collects environment variables for the given keys.
func CollectEnvVars(keys []string) map[string]string {
	result := make(map[string]string)
	for _, key := range keys {
		if val := os.Getenv(key); val != "" {
			result[key] = val
		}
	}
	return result
}
