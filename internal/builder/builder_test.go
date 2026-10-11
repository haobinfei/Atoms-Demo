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

func TestBuildRewritesSingleQuotedEntryScript(t *testing.T) {
	dir := t.TempDir()
	files := provider.FileSet{
		"index.html": `<!doctype html>
<html lang='zh-CN'>
<head><title>我的博客</title></head>
<body><div id='root'></div><script type='module' src='/src/main.tsx'></script></body>
</html>`,
		"src/main.tsx": `import { createRoot } from 'react-dom/client'
function App() { return <main>博客内容</main> }
createRoot(document.getElementById('root')!).render(<App />)`,
	}

	distDir := filepath.Join(dir, "dist")
	if err := builder.New().Build(writeApp(t, dir, files), distDir); err != nil {
		t.Fatalf("Build: %v", err)
	}
	index, err := os.ReadFile(filepath.Join(distDir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(index), "/src/main.tsx") {
		t.Fatalf("dist/index.html still references source entry: %s", index)
	}
	if !strings.Contains(string(index), `src='./main.js'`) {
		t.Fatalf("dist/index.html does not reference emitted bundle: %s", index)
	}
	if strings.Count(string(index), "src='./main.js'") != 1 {
		t.Fatalf("dist/index.html must reference the emitted bundle exactly once: %s", index)
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

func TestBuildRecoversMissingCSSImports(t *testing.T) {
	dir := t.TempDir()
	// App imports a stylesheet the "LLM" never emitted.
	files := provider.FileSet{
		"index.html":   `<!DOCTYPE html><html><head></head><body><div id="root"></div><script type="module" src="/src/main.tsx"></script></body></html>`,
		"src/main.tsx": `import App from "./App"; import "./styles.css";`,
	}
	appDir := writeApp(t, dir, files)
	// Import from a nested component too, to exercise importer-relative paths.
	if err := os.MkdirAll(filepath.Join(appDir, "src", "components"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "src", "components", "Card.tsx"),
		[]byte(`import "../panel.css"; export default function Card() { return <div/> }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "src", "App.tsx"),
		[]byte(`import Card from "./components/Card"; export default function App() { return <Card/> }`), 0o644); err != nil {
		t.Fatal(err)
	}

	b := builder.New()
	distDir := filepath.Join(dir, "dist")
	if err := b.Build(appDir, distDir); err != nil {
		t.Fatalf("Build should recover missing css: %v", err)
	}
	// The reported import gets a stub next to the importer. (esbuild only
	// surfaces entry-graph css failures and silently externals nested ones,
	// so ../panel.css neither errors nor gets stubbed — build still works.)
	for _, stub := range []string{"src/styles.css"} {
		content, err := os.ReadFile(filepath.Join(appDir, stub))
		if err != nil {
			t.Fatalf("stub %s not written: %v", stub, err)
		}
		if len(content) == 0 {
			t.Fatalf("stub %s is empty", stub)
		}
	}
}

// Regression: LLMs sometimes invent a bare "atomspay:atomspay" specifier and
// hallucinated export names like simulatePayment. The platform plugin must
// resolve every such spelling to the injected SDK so the build still passes.
func TestBuildToleratesHallucinatedAtomsPayImports(t *testing.T) {
	files := provider.FileSet{
		"index.html": "<html><body><div id=\"root\"></div></body></html>",
		"src/main.tsx": "import { simulatePayment } from 'atomspay:atomspay'\n" +
			"export default function A(){ console.log(simulatePayment); return <div/>}",
	}
	dir := t.TempDir()
	if err := builder.New().Build(writeApp(t, dir, files), filepath.Join(dir, "dist")); err != nil {
		t.Fatalf("hallucinated atomspay import should still build: %v", err)
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
