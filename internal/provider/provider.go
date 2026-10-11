// Package provider abstracts the 生成引擎 (Provider): the pluggable backend
// that turns a user prompt into a FileSet. Implementations: DeepSeekProvider
// (primary), KimiProvider, LocalProvider (local gemma), FakeProvider and
// TemplateProvider (deterministic test fixtures). ADR-0008 records the
// no-template-fallback policy.
package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// FileSet is a generated application: relative path → file content.
type FileSet map[string]string

// Provider turns a prompt into files. Generate may emit progress through
// the emit callback before returning the final FileSet.
type Provider interface {
	Name() string
	Generate(ctx context.Context, prompt string, emit func(stage string, detail string)) (FileSet, error)
}

var allowedExts = map[string]bool{
	".tsx": true, ".ts": true, ".jsx": true, ".js": true,
	".css": true, ".html": true, ".json": true, ".svg": true,
}

var allowedImports = map[string]bool{
	"react": true, "react-dom": true, "react-dom/client": true, "react/jsx-runtime": true,
}

var importRE = regexp.MustCompile(`(?m)from\s+["']([^"']+)["']`)
var sideEffectImportRE = regexp.MustCompile(`(?m)^\s*import\s+["']([^"']+)["']`)

// Validate enforces the Q10 contract: safe paths, known extensions, and
// imports restricted to react / react-dom plus relative modules.
func Validate(fs FileSet) error {
	if len(fs) == 0 {
		return fmt.Errorf("empty file set")
	}
	if _, ok := fs["index.html"]; !ok {
		return fmt.Errorf("entry file index.html missing")
	}
	if _, ok := fs["src/main.tsx"]; !ok {
		return fmt.Errorf("entry file src/main.tsx missing")
	}
	for path, content := range fs {
		if strings.Contains(path, "..") || filepath.IsAbs(path) || strings.HasPrefix(path, "/") {
			return fmt.Errorf("unsafe path %q", path)
		}
		if !allowedExts[strings.ToLower(filepath.Ext(path))] {
			return fmt.Errorf("disallowed extension in %q", path)
		}
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("empty content in %q", path)
		}
		for _, re := range []*regexp.Regexp{importRE, sideEffectImportRE} {
			for _, m := range re.FindAllStringSubmatch(content, -1) {
				imp := m[1]
				if strings.HasPrefix(imp, ".") {
					continue
				}
				if !allowedImports[imp] {
					return fmt.Errorf("disallowed import %q in %s", imp, path)
				}
			}
		}
	}
	return nil
}

// FakeProvider returns a fixed three-file React project, emitting one stage
// per file so pipelines can be exercised end to end.
type FakeProvider struct{}

func (FakeProvider) Name() string { return "fake" }

func (FakeProvider) Generate(_ context.Context, _ string, emit func(stage string, detail string)) (FileSet, error) {
	files := FileSet{
		"src/App.tsx":  "export default function App() {\n  return <h1>Hello from FakeProvider</h1>\n}\n",
		"src/main.tsx": "import { createRoot } from 'react-dom/client'\nimport App from './App'\ncreateRoot(document.getElementById('root')!).render(<App />)\n",
		"index.html":   "<!doctype html>\n<html><body><div id=\"root\"></div></body></html>\n",
	}
	return files, nil
}
