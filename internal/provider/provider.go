// Package provider abstracts the 生成引擎 (Provider): the pluggable backend
// that turns a user prompt into a FileSet. Real implementations (DeepSeek,
// local gemma, template fallback) land in a later slice; FakeProvider exists
// so the end-to-end skeleton is testable without any LLM.
package provider

// FileSet is a generated application: relative path → file content.
type FileSet map[string]string

// Provider turns a prompt into files. Generate may emit progress through
// the emit callback before returning the final FileSet.
type Provider interface {
	Name() string
	Generate(prompt string, emit func(stage string, detail string)) (FileSet, error)
}

// FakeProvider returns a fixed three-file React project, emitting one stage
// per file so pipelines can be exercised end to end.
type FakeProvider struct{}

func (FakeProvider) Name() string { return "fake" }

func (FakeProvider) Generate(_ string, emit func(stage string, detail string)) (FileSet, error) {
	files := FileSet{
		"src/App.tsx":  "export default function App() {\n  return <h1>Hello from FakeProvider</h1>\n}\n",
		"src/main.tsx": "import { createRoot } from 'react-dom/client'\nimport App from './App'\ncreateRoot(document.getElementById('root')!).render(<App />)\n",
		"index.html":   "<!doctype html>\n<html><body><div id=\"root\"></div></body></html>\n",
	}
	for name := range files {
		emit("file", name)
	}
	return files, nil
}
