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
	"regexp"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/haobinfei/Atoms-Demo/internal/provider"
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
	options := func() api.BuildOptions {
		return api.BuildOptions{
			EntryPoints:      []string{filepath.Join(appDir, "src", "main.tsx")},
			Outdir:           distDir,
			Bundle:           true,
			Write:            true,
			Format:           api.FormatESModule,
			JSX:              api.JSXAutomatic,
			MinifySyntax:     true,
			MinifyWhitespace: true,
			LogLevel:         api.LogLevelSilent,
			Plugins:          []api.Plugin{platformPlugin()},
			AbsWorkingDir:    appDir,
			OutExtension:     map[string]string{".js": ".js"},
			EntryNames:       "[name]",
			ChunkNames:       "[name]",
		}
	}
	result := api.Build(options())
	// LLMs often import stylesheets they forgot to emit. esbuild surfaces
	// only part of the unresolved-import graph per pass, so stub every
	// reported .css and rebuild, a few rounds, until the build is clean or
	// something real is missing (a non-CSS import cannot be stubbed away).
	for round := 0; len(result.Errors) > 0 && round < 4; round++ {
		stubs, ok := missingCSS(result.Errors, appDir)
		if !ok {
			break
		}
		for _, stub := range stubs {
			if err := os.WriteFile(stub, []byte("/* recovered: LLM 未生成此文件，构建器补空 */\n"), 0o644); err != nil {
				return fmt.Errorf("build failed: %s", joinErrors(result.Errors))
			}
		}
		result = api.Build(options())
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("build failed: %s", joinErrors(result.Errors))
	}

	return b.rewriteIndex(appDir, distDir)
}

func joinErrors(errs []api.Message) string {
	msgs := make([]string, 0, len(errs))
	for _, m := range errs {
		msgs = append(msgs, m.Text)
	}
	return strings.Join(msgs, "; ")
}

var unresolvedRE = regexp.MustCompile(`Could not resolve "([^"]+)"`)

// missingCSS inspects esbuild resolution errors: if EVERY unresolved
// import is a .css path, it returns the stub paths to create (next to
// the importing file, resolved against appDir — esbuild reports importer
// paths relative to AbsWorkingDir) and true. Any non-CSS unresolved
// import means real code is missing — no stub can save that build.
func missingCSS(errs []api.Message, appDir string) ([]string, bool) {
	stubs := make([]string, 0, len(errs))
	seen := map[string]bool{}
	for _, m := range errs {
		match := unresolvedRE.FindStringSubmatch(m.Text)
		if match == nil || !strings.HasSuffix(match[1], ".css") {
			return nil, false
		}
		dir := "."
		if m.Location != nil && m.Location.File != "" {
			dir = filepath.Dir(m.Location.File)
		}
		p := filepath.Join(appDir, dir, match[1])
		if !seen[p] {
			seen[p] = true
			stubs = append(stubs, p)
		}
	}
	return stubs, len(stubs) > 0
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

// platformPlugin resolves react and react-dom/client from the embedded
// 供应商目录 instead of node_modules, and answers './atomspay' imports
// with the platform-injected AtomsPay SDK so every build path (pipeline,
// contest, bare builder) can compile ecommerce apps.
func platformPlugin() api.Plugin {
	return api.Plugin{
		Name: "atoms-vendor",
		Setup: func(build api.PluginBuild) {
			// 兼容 LLM 臆造的各种写法：'./atomspay'、'atomspay'、
			// 'atomspay:atomspay'、'/atomspay.ts' 一律解析到平台注入的 SDK。
			build.OnResolve(api.OnResolveOptions{Filter: `^\.?/?atomspay(:atomspay)?(\.ts)?$`},
				func(args api.OnResolveArgs) (api.OnResolveResult, error) {
					return api.OnResolveResult{Path: "atomspay", Namespace: "atomspay"}, nil
				})
			build.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "atomspay"}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
				str := provider.AtomsPaySDK
				return api.OnLoadResult{Contents: &str, Loader: api.LoaderTS}, nil
			})
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
