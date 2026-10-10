// OpenAI-compatible chat client shared by DeepSeekProvider and LocalProvider
// (the local gemma server speaks the same /v1 protocol).
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	MaxTokens      int           `json:"max_tokens,omitempty"`
	Temperature    *float64      `json:"temperature,omitempty"`
	ResponseFormat *struct {
		Type string `json:"type"`
	} `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// openAIClient is a minimal Chat Completions client.
type openAIClient struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
	// temperature overrides the request default (0.4); some endpoints
	// (Kimi Code) reject any value but their fixed one.
	temperature *float64
}

func (c *openAIClient) chat(ctx context.Context, messages []chatMessage, maxTokens int, jsonMode bool) (string, error) {
	reqBody := chatRequest{
		Model:     c.model,
		Messages:  messages,
		MaxTokens: maxTokens,
	}
	if c.temperature != nil {
		reqBody.Temperature = c.temperature
	} else {
		t := 0.4
		reqBody.Temperature = &t
	}
	if jsonMode {
		reqBody.ResponseFormat = &struct {
			Type string `json:"type"`
		}{Type: "json_object"}
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s request: %w", c.model, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s status %d: %.200s", c.model, resp.StatusCode, body)
	}
	var out chatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("%s decode: %w", c.model, err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", fmt.Errorf("%s: %s", c.model, out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("%s returned no choices", c.model)
	}
	content := out.Choices[0].Message.Content
	if content == "" && out.Choices[0].Message.ReasoningContent != "" {
		return "", fmt.Errorf("%s spent its token budget on reasoning (finish=%q); raise max_tokens",
			c.model, out.Choices[0].FinishReason)
	}
	return content, nil
}

func defaultHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// parseFileSet extracts {"files": {path: content}} from an LLM reply,
// tolerating markdown fences and a bare top-level map.
func parseFileSet(raw string) (FileSet, error) {
	s := raw
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
	}
	var wrapped struct {
		Files map[string]string `json:"files"`
	}
	if err := json.Unmarshal([]byte(s), &wrapped); err == nil && len(wrapped.Files) > 0 {
		return FileSet(wrapped.Files), nil
	}
	var flat map[string]string
	if err := json.Unmarshal([]byte(s), &flat); err == nil && len(flat) > 0 {
		return FileSet(flat), nil
	}
	return nil, fmt.Errorf("no file set found in model output (%.120s…)", raw)
}
