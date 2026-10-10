// Package builder is the 构建器: it compiles a session's file set (React +
// TypeScript) into a runnable static bundle with esbuild. React and
// React-dom resolve from the go:embed 供应商目录 (vendor/), so builds need
// zero network access (Q11 decision).
package builder

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

//go:embed vendor
var vendorFS embed.FS

type Builder struct{}

func New() *Builder { return &Builder{} }

const (
	entryJS  = "main.js"
	entryCSS = "main.css"
)

// Build bundles appDir/src/main.tsx into distDir (main.js + main.css) and
// rewrites the app's index.html to load them.
func (b *Builder) Build(appDir, distDir string) error {
	// esbuild requires an absolute working directory; the server's default
	// workspace dir is relative ("workspace").
	abs, err := filepath.Abs(appDir)
	if err != nil {
		return err
	}
	appDir = abs
	if distDir, err = filepath.Abs(distDir); err != nil {
		return err
	}
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		return err
	}
	result := api.Build(api.BuildOptions{
		EntryPoints:      []string{filepath.Join(appDir, "src", "main.tsx")},
		Outdir:           distDir,
		Bundle:           true,
		Write:            true,
		Format:           api.FormatESModule,
		JSX:              api.JSXAutomatic,
		MinifySyntax:     true,
		MinifyWhitespace: true,
		LogLevel:         api.LogLevelSilent,
		Plugins:          []api.Plugin{vendorPlugin()},
		AbsWorkingDir:    appDir,
		OutExtension:     map[string]string{".js": ".js"},
		EntryNames:       "[name]",
		ChunkNames:       "[name]",
	})
	if len(result.Errors) > 0 {
		msgs := make([]string, 0, len(result.Errors))
		for _, m := range result.Errors {
			msgs = append(msgs, m.Text)
		}
		return fmt.Errorf("build failed: %s", strings.Join(msgs, "; "))
	}

	return b.rewriteIndex(appDir, distDir)
}

// rewriteIndex points the app's index.html at the emitted bundle.
func (b *Builder) rewriteIndex(appDir, distDir string) error {
	raw, err := os.ReadFile(filepath.Join(appDir, "index.html"))
	if err != nil {
		return fmt.Errorf("read index.html: %w", err)
	}
	html := string(raw)
	if strings.Contains(html, "/src/main.tsx") {
		html = strings.Replace(html, `src="/src/main.tsx"`, `src="./`+entryJS+`"`, 1)
		html = strings.Replace(html, `src="./src/main.tsx"`, `src="./`+entryJS+`"`, 1)
	} else if strings.Contains(html, "</body>") {
		html = strings.Replace(html, "</body>",
			`<script type="module" src="./`+entryJS+`"></script></body>`, 1)
	} else {
		html += `<script type="module" src="./` + entryJS + `"></script>`
	}
	if _, err := os.Stat(filepath.Join(distDir, entryCSS)); err == nil &&
		!strings.Contains(html, entryCSS) {
		link := `<link rel="stylesheet" href="./` + entryCSS + `" />`
		if strings.Contains(html, "</head>") {
			html = strings.Replace(html, "</head>", link+"</head>", 1)
		} else if strings.Contains(html, "</body>") {
			html = strings.Replace(html, "</body>", link+"</body>", 1)
		} else {
			html += link
		}
	}
	return os.WriteFile(filepath.Join(distDir, "index.html"), []byte(html), 0o644)
}

// vendorPlugin resolves react and react-dom/client from the embedded
// 供应商目录 instead of node_modules.
func vendorPlugin() api.Plugin {
	return api.Plugin{
		Name: "atoms-vendor",
		Setup: func(build api.PluginBuild) {
			vendored := map[string]string{
				"react":             "react.js",
				"react/jsx-runtime": "react-jsx-runtime.js",
				"react-dom":         "react-dom.js",
				"react-dom/client":  "react-dom-client.js",
				"scheduler":         "scheduler.js",
			}
			build.OnResolve(api.OnResolveOptions{Filter: `^(react|react-dom|scheduler)`},
				func(args api.OnResolveArgs) (api.OnResolveResult, error) {
					file, ok := vendored[args.Path]
					if !ok {
						return api.OnResolveResult{}, nil
					}
					return api.OnResolveResult{Path: file, Namespace: "vendor"}, nil
				})
			build.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "vendor"}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
				contents, err := vendorFS.ReadFile("vendor/" + args.Path)
				if err != nil {
					return api.OnLoadResult{}, err
				}
				str := string(contents)
				return api.OnLoadResult{Contents: &str, Loader: api.LoaderJS}, nil
			})
		},
	}
}
