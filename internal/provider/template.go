// TemplateProvider is a deterministic test fixture: intent recognition over
// the prompt, then a per-category React template. It is deliberately not
// part of the production provider chain (ADR-0008).
package provider

import (
	"context"
	"fmt"
	"html"
	"regexp"
)

type TemplateProvider struct{}

func NewTemplate() *TemplateProvider { return &TemplateProvider{} }

func (p *TemplateProvider) Name() string { return "template" }

func (p *TemplateProvider) Generate(_ context.Context, prompt string, emit func(stage string, detail string)) (FileSet, error) {
	spec := Analyze(prompt)
	files := buildApp(spec)
	return files, nil
}

type promptIntent struct {
	category        string
	siteKind        string
	name            string
	hasCategory     bool
	hasExplicitName bool
	accent          string
	hasAccent       bool
}

// Spec is the recognized intent of a prompt.
type Spec struct {
	Category string // todo | dashboard | ecommerce | site
	SiteKind string // portfolio | restaurant | blog | landing (Category == "site")
	Name     string // app / brand name
	Accent   string // theme color hex
}

type categoryRule struct {
	category string
	siteKind string
	name     string
	re       *regexp.Regexp
}

var categoryRules = []categoryRule{
	{"todo", "", "轻记", regexp.MustCompile(`待办|todo|任务管理|清单|打卡|习惯|备忘|日程`)},
	{"dashboard", "", "数见", regexp.MustCompile(`看板|dashboard|仪表盘|数据|统计|报表|分析|图表|运营`)},
	{"ecommerce", "", "臻选", regexp.MustCompile(`电商|商店|商城|咖啡|花店|服装|美妆|店铺|购物|商品`)},
	{"site", "portfolio", "作品集", regexp.MustCompile(`作品集|portfolio|简历|个人主页|个人网站|设计师`)},
	{"site", "restaurant", "味集", regexp.MustCompile(`餐厅|美食|餐饮|菜谱|菜单|烘焙|奶茶|小吃`)},
	{"site", "blog", "漫笔", regexp.MustCompile(`博客|blog|写作|文章|周刊|newsletter`)},
}

var colorRules = []struct {
	re  *regexp.Regexp
	hex string
}{
	{regexp.MustCompile(`赛博|cyber|霓虹|neon`), "#22d3ee"},
	{regexp.MustCompile(`橙|橘|orange`), "#f97316"},
	{regexp.MustCompile(`蓝|blue`), "#3b82f6"},
	{regexp.MustCompile(`红|red`), "#ef4444"},
	{regexp.MustCompile(`绿|green`), "#22c55e"},
	{regexp.MustCompile(`紫|purple`), "#8b5cf6"},
	{regexp.MustCompile(`粉|pink`), "#ec4899"},
	{regexp.MustCompile(`青|cyan|teal`), "#14b8a6"},
	{regexp.MustCompile(`黄|yellow`), "#eab308"},
}

var defaultAccent = "#f97316"

var nameRE = regexp.MustCompile(`[《「『"“']([^《》「」』"”']{2,16})[》」』"”']`)

// Analyze maps a prompt to the closest template category.
func Analyze(prompt string) Spec {
	intentText, latest := promptIntentText(prompt)
	intent := detectIntent(intentText)
	if latest != "" {
		mergeIntent(&intent, detectIntent(latest))
	}
	spec := Spec{Category: "site", SiteKind: "landing", Name: "Nexus", Accent: defaultAccent}
	if intent.hasCategory {
		spec.Category = intent.category
		spec.SiteKind = intent.siteKind
	}
	if intent.name != "" {
		spec.Name = intent.name
	}
	if intent.hasAccent {
		spec.Accent = intent.accent
	}
	return spec
}

// detectIntent uses the last matching request, not the first rule in the
// table. That makes “先改成蓝色，再改成红色” resolve to red instead of the
// stale blue request (and prevents current file contents from winning).
func detectIntent(text string) promptIntent {
	var out promptIntent
	categoryAt := -1
	for _, rule := range categoryRules {
		if at := lastMatchIndex(rule.re, text); at > categoryAt {
			categoryAt = at
			out.category = rule.category
			out.siteKind = rule.siteKind
			out.name = rule.name
			out.hasCategory = true
		}
	}
	accentAt := -1
	for _, c := range colorRules {
		if at := lastMatchIndex(c.re, text); at > accentAt {
			accentAt = at
			out.accent = c.hex
			out.hasAccent = true
		}
	}
	// “赛博朋克 + 霓虹青色” is one visual direction, not two competing
	// colors. Treat the cyber keyword as the stronger signal so the later
	// generic “青” rule cannot downgrade the accent to plain teal.
	if cyberAt := lastMatchIndex(colorRules[0].re, text); cyberAt >= 0 {
		out.accent = colorRules[0].hex
		out.hasAccent = true
	}
	if matches := nameRE.FindAllStringSubmatchIndex(text, -1); len(matches) > 0 {
		m := matches[len(matches)-1]
		out.name = text[m[2]:m[3]]
		out.hasExplicitName = true
	}
	return out
}

func mergeIntent(base *promptIntent, latest promptIntent) {
	if latest.hasCategory {
		base.category = latest.category
		base.siteKind = latest.siteKind
		base.hasCategory = true
		if !base.hasExplicitName {
			base.name = latest.name
		}
	}
	if latest.hasExplicitName {
		base.name = latest.name
		base.hasExplicitName = true
	}
	if latest.hasAccent {
		base.accent = latest.accent
		base.hasAccent = true
	}
}

func lastMatchIndex(re *regexp.Regexp, text string) int {
	matches := re.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return -1
	}
	return matches[len(matches)-1][0]
}

func buildApp(spec Spec) FileSet {
	var app, css string
	switch spec.Category {
	case "todo":
		app, css = todoApp(spec)
	case "dashboard":
		app, css = dashboardApp(spec)
	case "ecommerce":
		app, css = ecommerceApp(spec)
	default:
		app, css = siteApp(spec)
	}
	return FileSet{
		"index.html":     indexHTML(spec.Name, spec.Accent),
		"src/main.tsx":   mainTSX(),
		"src/App.tsx":    app,
		"src/styles.css": css,
	}
}

func indexHTML(title, accent string) string {
	title = html.EscapeString(title)
	return fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>%s</title>
    <meta name="description" content="%s —— 由 Atoms Demo 生成" />
    <style>body{margin:0;background:#0f1115}</style>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
`, title, title)
}

func mainTSX() string {
	return `import { createRoot } from 'react-dom/client'
import App from './App'
import './styles.css'

createRoot(document.getElementById('root')!).render(<App />)
`
}
