// OpenAI-compatible chat client shared by DeepSeekProvider and LocalProvider
// (the local gemma server speaks the same /v1 protocol).
package provider

import (
	"bufio"
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
	// Stream keeps bytes flowing while the model writes. Long app
	// generations otherwise sit silent past every proxy's idle timeout
	// (Cloudflare 524, http.Client.Timeout) even when the model is fine.
	Stream bool `json:"stream,omitempty"`
}

// chatChunk is one SSE delta from a streaming completion.
type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
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
	// stream requests SSE deltas and reassembles them locally.
	stream bool
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
	reqBody.Stream = c.stream
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
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return "", fmt.Errorf("%s status %d: %.200s", c.model, resp.StatusCode, body)
	}
	// A gateway may answer a stream request with a plain JSON body (test
	// doubles and some aggregators do); fall through to the batch parser.
	if c.stream && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return c.readStream(resp.Body)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
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

// readStream reassembles an SSE completion. Only delta.content is kept;
// reasoning deltas (DeepSeek's chain-of-thought) are not part of the reply.
func (c *openAIClient) readStream(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	var content strings.Builder
	var sawReasoning bool
	var finish string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return "", fmt.Errorf("%s: %s", c.model, chunk.Error.Message)
		}
		for _, choice := range chunk.Choices {
			content.WriteString(choice.Delta.Content)
			if choice.Delta.ReasoningContent != "" {
				sawReasoning = true
			}
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("%s stream: %w", c.model, err)
	}
	out := content.String()
	if out == "" {
		if sawReasoning {
			return "", fmt.Errorf("%s spent its token budget on reasoning (finish=%q); raise max_tokens", c.model, finish)
		}
		return "", fmt.Errorf("%s stream ended without content (finish=%q)", c.model, finish)
	}
	return out, nil
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
