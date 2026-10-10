package server_test

import (
	"context"
	"strings"
	"testing"

	"github.com/haobinfei/Atoms-Demo/internal/provider"
	"github.com/haobinfei/Atoms-Demo/internal/server"
)

// replyProvider returns one fixed file as the LLM reply, whatever the prompt.
type replyProvider struct{ reply string }

func (r replyProvider) Name() string { return "reply" }

func (r replyProvider) Generate(_ context.Context, _ string, _ func(string, string)) (provider.FileSet, error) {
	return provider.FileSet{"reply.txt": r.reply}, nil
}

func TestNinaScoresViaLLM(t *testing.T) {
	files := provider.FileSet{"src/App.tsx": "export default function App() { return <h1>hi</h1> }"}
	cases := []struct {
		name    string
		reply   string
		wantErr bool
		design  int
	}{
		{"clean json", `{"design":8,"functionality":7,"codeQuality":9,"comment":"结构清晰"}`, false, 8},
		{"prose around json", `评审结果：{"design":6,"functionality":5,"codeQuality":7,"comment":"尚可"}。以上。`, false, 6},
		{"out of range clamps", `{"design":42,"functionality":-3,"codeQuality":9,"comment":"分数越界需截断"}`, false, 10},
		{"no json", `评分完成，结果不错`, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fn := server.DefaultScoreFunc(replyProvider{reply: tc.reply})
			scores, comment, err := fn(context.Background(), "一个数据看板", files)
			if tc.wantErr {
				// The default wrapper falls back to the heuristic instead
				// of erroring, so a garbled LLM reply never wedges scoring.
				if err != nil {
					t.Fatalf("fallback scorer errored: %v", err)
				}
				if scores.Design == 42 {
					t.Fatal("unclamped score leaked through")
				}
				return
			}
			if err != nil {
				t.Fatalf("score: %v", err)
			}
			if scores.Design != tc.design {
				t.Fatalf("design = %d, want %d", scores.Design, tc.design)
			}
			if comment == "" {
				t.Fatal("comment is empty")
			}
		})
	}
}

func TestHeuristicScoreDeterministic(t *testing.T) {
	fn := server.DefaultScoreFunc(nil)
	files := provider.FileSet{
		"index.html":   "<div id=\"root\"></div>",
		"src/App.tsx":  strings.Repeat("line\n", 100),
		"src/main.tsx": "import { createRoot } from 'react-dom/client'",
	}
	a, _, err := fn(context.Background(), "prompt", files)
	if err != nil {
		t.Fatalf("heuristic: %v", err)
	}
	b, _, err := fn(context.Background(), "prompt", files)
	if err != nil {
		t.Fatalf("heuristic again: %v", err)
	}
	if a != b {
		t.Fatalf("heuristic not deterministic: %+v vs %+v", a, b)
	}
	if a.Design > 10 || a.Functionality > 10 || a.CodeQuality > 10 {
		t.Fatalf("scores out of range: %+v", a)
	}
}
