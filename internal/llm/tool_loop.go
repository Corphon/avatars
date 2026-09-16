package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// runNonStreamingToolLoop executes a tool-calling conversation loop using
// non-streaming HTTP requests. It starts at the given turn index and
// continues until the model returns text without tool calls or maxToolTurns
// is reached.
//
// This is shared by both the pure non-streaming path (startTurn=0) and the
// streaming→non-streaming hybrid path (startTurn=1, after streaming already
// handled turn 0 and executed the first round of tool calls).
func (c *OpenAICompatibleClient) runNonStreamingToolLoop(
	ctx context.Context,
	resolved resolvedOpenAICompatibleRequest,
	model string,
	request Request,
	messages []openAIMessage,
	startTurn int,
	allToolCalls []ToolCall,
	totalUsage openAICompatibleUsage,
	httpClient *http.Client,
) (Response, error) {
	maxToolTurns := request.MaxToolTurns
	if maxToolTurns <= 0 {
		maxToolTurns = 5 // fallback default
	}

	thinkMode := effectiveThinkMode(c.config.ThinkMode, request)
	thinkingOn := thinkingEnabledForRequest(c.Provider(), model, thinkMode)

	for turn := startTurn; turn < maxToolTurns; turn++ {
		reqPayload := openAIRequest{
			Model:       model,
			Messages:    messages,
			Stream:      false,
			Temperature: 0,
		}
		if mt := c.cappedMaxTokens(request); mt > 0 {
			reqPayload.MaxTokens = mt
		}
		// Web search support for Qwen/DeepSeek/OpenRouter.
		if request.UseWebSearch && c.config.EnableWebSearch {
			switch c.preset.WebSearchMode {
			case webSearchModeEnableSearch:
				reqPayload.EnableSearch = true
			case webSearchModePluginsWeb:
				reqPayload.Plugins = []map[string]string{{"id": "web"}}
			}
		}

		// Structured output with json_schema when schema is provided.
		applyStructuredOutputToOpenAIRequest(&reqPayload, request, turn)

		// Pass tools every turn (Z8). S5.2 shared helper.
		applyToolsToOpenAIRequest(&reqPayload, request, turn)
		applyThinkingToOpenAIRequest(c.Provider(), thinkMode, &reqPayload)

		body, err := json.Marshal(reqPayload)
		if err != nil {
			return Response{}, fmt.Errorf("marshal openai-compatible request: %w", err)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, resolved.endpoint, bytes.NewReader(body))
		if err != nil {
			return Response{}, fmt.Errorf("build openai-compatible request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if resolved.apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+resolved.apiKey)
		}

		httpResponse, err := httpClient.Do(httpReq)
		if err != nil {
			return Response{}, fmt.Errorf("openai-compatible request failed: %w", err)
		}
		responseBody, readErr := io.ReadAll(httpResponse.Body)
		httpResponse.Body.Close()
		if readErr != nil {
			return Response{Usage: usageFromPartialBody(responseBody)}, fmt.Errorf("read openai-compatible response: %w", readErr)
		}

		var completion openAICompatibleChatCompletionResponse
		if err := json.Unmarshal(responseBody, &completion); err != nil {
			return Response{}, fmt.Errorf("decode openai-compatible response: %w", err)
		}
		if httpResponse.StatusCode >= http.StatusBadRequest {
			if completion.Error.Message != "" {
				return Response{}, fmt.Errorf("openai-compatible provider %q returned %d: %s", c.Provider(), httpResponse.StatusCode, completion.Error.Message)
			}
			return Response{}, fmt.Errorf("openai-compatible provider %q returned %d", c.Provider(), httpResponse.StatusCode)
		}
		if len(completion.Choices) == 0 {
			return Response{}, fmt.Errorf("openai-compatible provider %q returned no choices", c.Provider())
		}

		// Accumulate usage across turns.
		totalUsage.add(completion.Usage)

		msg := completion.Choices[0].Message

		// Detect tool calls: native tool_calls or text-based fallback.
		nativeToolCalls := msg.ToolCalls
		textContent := extractOpenAIContent(msg.Content)

		if request.ThinkingCallback != nil && strings.TrimSpace(msg.ReasoningContent) != "" {
			request.ThinkingCallback(msg.ReasoningContent)
		}
		if request.StreamCallback != nil {
			if textContent != "" {
				request.StreamCallback(textContent)
			}
			for _, tc := range nativeToolCalls {
				if args := tc.Function.Arguments; args != "" {
					request.StreamCallback(args)
				} else if name := tc.Function.Name; name != "" {
					request.StreamCallback(name)
				}
			}
		}

		if len(nativeToolCalls) == 0 && textContent != "" {
			parsed := parseToolCallsRaw(textContent)
			if len(parsed) > 0 {
				nativeToolCalls = parsed
			} else {
				// Fallback: try comprehensive text parser for Chinese XML,
				// Anthropic-style, and JSON-block tool call formats that
				// parseToolCallsRaw doesn't handle.
				parsed = parseTextToolCalls(textContent)
				if len(parsed) > 0 {
					nativeToolCalls = parsed
				}
			}
		}

		// Check for tool calls.
		if len(nativeToolCalls) > 0 {
			// Append assistant message with tool calls. Z8: always pin
			// reasoning_content for DeepSeek/Kimi thinking+tools replay.
			messages = append(messages, openAIMessage{
				Role:             "assistant",
				Content:          textContent,
				ReasoningContent: reasoningContentForToolReplay(c.Provider(), thinkingOn, msg.ReasoningContent),
				ToolCalls:        nativeToolCalls,
			})
			// Execute tools and append tool result messages.
			for _, tc := range nativeToolCalls {
				var callArgs map[string]any
				if argsErr := json.Unmarshal([]byte(tc.Function.Arguments), &callArgs); argsErr == nil {
					allToolCalls = append(allToolCalls, ToolCall{
						ID:        tc.ID,
						Name:      tc.Function.Name,
						Arguments: callArgs,
					})
				}
				messages = append(messages, openAIMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    executeOpenAIToolCall(ctx, tc),
				})
			}
			continue // next turn
		}

		// No tool calls — return the text response.
		text := strings.TrimSpace(extractOpenAIContent(msg.Content))
		if text == "" && turn == 0 {
			text = buildDeterministicFallback(request)
		}

		return Response{
			Text:         text,
			Reasoning:    strings.TrimSpace(msg.ReasoningContent),
			ToolCalls:    allToolCalls,
			FinishReason: NormalizeFinishReason(c.Provider(), completion.Choices[0].FinishReason),
			Provider:     c.Provider(),
			Model:        resolved.model,
			Fallback:     false,
			Usage:        chunkToUsage(&totalUsage),
		}, nil
	}

	return Response{}, fmt.Errorf("openai-compatible: exceeded max tool turns (%d)", maxToolTurns)
}
