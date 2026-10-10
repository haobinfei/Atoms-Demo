// TemplateProvider is the offline 兜底 engine: intent recognition over the
// prompt, then a per-category React template (ADR-0002). It guarantees a
// buildable, on-topic app with zero network.
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
	spec := Spec{Category: "site", SiteKind: "landing", Name: "Nexus", Accent: defaultAccent}
	for _, rule := range categoryRules {
		if rule.re.MatchString(prompt) {
			spec.Category = rule.category
			spec.SiteKind = rule.siteKind
			spec.Name = rule.name
			break
		}
	}
	for _, c := range colorRules {
		if c.re.MatchString(prompt) {
			spec.Accent = c.hex
			break
		}
	}
	if m := nameRE.FindStringSubmatch(prompt); m != nil {
		spec.Name = m[1]
	}
	return spec
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
