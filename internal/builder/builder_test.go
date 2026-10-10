package builder_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/builder"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
)

// writeApp materializes a FileSet under dir/app.
func writeApp(t *testing.T, dir string, files provider.FileSet) string {
	t.Helper()
	appDir := filepath.Join(dir, "app")
	for path, content := range files {
		full := filepath.Join(appDir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return appDir
}

func TestBuildProducesRunnableDist(t *testing.T) {
	dir := t.TempDir()
	files, err := provider.NewTemplate().Generate(t.Context(), "一家咖啡店的电商网站", func(string, string) {})
	if err != nil {
		t.Fatal(err)
	}
	appDir := writeApp(t, dir, files)

	b := builder.New()
	distDir := filepath.Join(dir, "dist")
	if err := b.Build(appDir, distDir); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// index.html rewritten to load the bundle and keep the mount point.
	index, err := os.ReadFile(filepath.Join(distDir, "index.html"))
	if err != nil {
		t.Fatalf("dist/index.html missing: %v", err)
	}
	if !strings.Contains(string(index), `id="root"`) {
		t.Fatal("dist/index.html lost the root mount point")
	}
	if !strings.Contains(string(index), "./main.js") {
		t.Fatal("dist/index.html does not reference the bundle")
	}
	if !strings.Contains(string(index), "./main.css") {
		t.Fatal("dist/index.html does not reference the stylesheet")
	}

	// The bundle is self-contained: react is embedded, not imported.
	js, err := os.ReadFile(filepath.Join(distDir, "main.js"))
	if err != nil {
		t.Fatalf("dist/main.js missing: %v", err)
	}
	if strings.Contains(string(js), `from"react"`) || strings.Contains(string(js), `from "react"`) {
		t.Fatal("bundle still imports react externally")
	}
	if !strings.Contains(string(js), "createRoot") {
		t.Fatal("bundle does not contain react-dom client runtime")
	}
	if _, err := os.Stat(filepath.Join(distDir, "main.css")); err != nil {
		t.Fatalf("dist/main.css missing: %v", err)
	}
}

func TestBuildFailsOnSyntaxErrorAndUnknownImport(t *testing.T) {
	b := builder.New()

	syntax := provider.FileSet{
		"index.html":   "<html><body><div id=\"root\"></div></body></html>",
		"src/main.tsx": "this is not valid typescript((",
	}
	dir := t.TempDir()
	if err := b.Build(writeApp(t, dir, syntax), filepath.Join(dir, "dist")); err == nil {
		t.Fatal("syntax error not reported")
	}

	badImport := provider.FileSet{
		"index.html": "<html><body><div id=\"root\"></div></body></html>",
		// The import must be *used*: esbuild (like tsc) silently elides
		// unused TS imports, which would defeat resolution errors.
		"src/main.tsx": "import _ from 'lodash'\nexport default function A(){ console.log(_); return <div/>}",
	}
	dir2 := t.TempDir()
	err := b.Build(writeApp(t, dir2, badImport), filepath.Join(dir2, "dist"))
	if err == nil || !strings.Contains(err.Error(), "lodash") {
		t.Fatalf("unknown import not reported with detail: %v", err)
	}
}

// Regression: the server's default workspace dir is relative ("workspace"),
// and esbuild rejects a non-absolute AbsWorkingDir.
func TestBuildWithRelativePaths(t *testing.T) {
	parent := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(parent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	files, err := provider.NewTemplate().Generate(t.Context(), "一个待办应用", func(string, string) {})
	if err != nil {
		t.Fatal(err)
	}
	appDir := writeApp(t, ".", files)
	if err := builder.New().Build(appDir, "dist"); err != nil {
		t.Fatalf("relative-path build: %v", err)
	}
	if _, err := os.Stat("dist/index.html"); err != nil {
		t.Fatalf("dist/index.html missing: %v", err)
	}
}
