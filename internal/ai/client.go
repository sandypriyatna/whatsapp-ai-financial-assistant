package ai

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

// Message is an exported conversation turn that callers can store and replay.
type Message struct {
	Role    string // "system" | "user" | "assistant"
	Content string
}

type LLMClient struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	model      string
}

type LLMResponse struct {
	Content   string
	ToolCalls []ToolCall
}

type ToolCall struct {
	Name      string
	Arguments json.RawMessage
}

func NewLLMClient(baseURL, apiKey, model string) *LLMClient {
	return &LLMClient{
		httpClient: &http.Client{Timeout: 35 * time.Second},
		baseURL:    strings.TrimSpace(baseURL),
		apiKey:     strings.TrimSpace(apiKey),
		model:      strings.TrimSpace(model),
	}
}

// Chat sends one system + one user message (single-turn, backward compat).
func (c *LLMClient) Chat(ctx context.Context, systemPrompt, userMessage string) (*LLMResponse, error) {
	msgs := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	}
	return c.ChatWithHistory(ctx, msgs)
}

// ChatWithHistory sends a full conversation history to the LLM.
// The first message should be role "system"; subsequent messages alternate
// between "user" and "assistant". Tool definitions are always included.
func (c *LLMClient) ChatWithHistory(ctx context.Context, messages []Message) (*LLMResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("llm client is nil")
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("baseURL is empty")
	}
	if c.apiKey == "" {
		return nil, fmt.Errorf("apiKey is empty")
	}
	if c.model == "" {
		return nil, fmt.Errorf("model is empty")
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("messages is empty")
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	apiMsgs := make([]openAIMessage, 0, len(messages))
	for _, m := range messages {
		apiMsgs = append(apiMsgs, openAIMessage{Role: m.Role, Content: m.Content})
	}

	payload := openAIChatRequest{
		Model:      c.model,
		Messages:   apiMsgs,
		Tools:      ToolDefinitions,
		ToolChoice: "auto",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := strings.TrimRight(c.baseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read llm response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm request returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed openAIChatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("failed to decode llm response json: %w", err)
	}

	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("empty llm response: no choices")
	}

	msg := parsed.Choices[0].Message
	result := &LLMResponse{
		Content: strings.TrimSpace(msg.Content),
	}

	// Prioritize tool calls if present.
	if len(msg.ToolCalls) > 0 {
		result.ToolCalls = make([]ToolCall, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			if strings.TrimSpace(tc.Function.Name) == "" {
				return nil, fmt.Errorf("invalid tool call: missing function name")
			}

			args := strings.TrimSpace(tc.Function.Arguments)
			if args == "" {
				args = "{}"
			}
			if !json.Valid([]byte(args)) {
				return nil, fmt.Errorf("invalid tool call arguments for %s: malformed json", tc.Function.Name)
			}

			result.ToolCalls = append(result.ToolCalls, ToolCall{
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(args),
			})
		}
		return result, nil
	}

	if result.Content == "" {
		return nil, fmt.Errorf("empty llm response: no content and no tool calls")
	}

	return result, nil
}

// --- internal OpenAI-compatible wire types ---

type openAIChatRequest struct {
	Model      string                   `json:"model"`
	Messages   []openAIMessage          `json:"messages"`
	Tools      []map[string]interface{} `json:"tools,omitempty"`
	ToolChoice string                   `json:"tool_choice,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatResponse struct {
	Choices []openAIChoice `json:"choices"`
}

type openAIChoice struct {
	Message openAIResponseMessage `json:"message"`
}

type openAIResponseMessage struct {
	Content   string           `json:"content"`
	ToolCalls []openAIToolCall `json:"tool_calls"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
