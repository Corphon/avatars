package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WebFetchInput describes a web page fetch request.
type WebFetchInput struct {
	URL     string `json:"url"`
	Timeout int    `json:"timeout_ms,omitempty"` // max wait in ms, default 15000
}

// WebFetchTool fetches a URL and returns its text content.
// It is safe for concurrent use (each call is an independent HTTP request).
type WebFetchTool struct{}

func (WebFetchTool) Name() string {
	return "webfetch"
}

func (WebFetchTool) IsConcurrencySafe(input any) bool {
	return true
}

func (t WebFetchTool) Call(ctx context.Context, input any) (Result, error) {
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	in, ok := input.(WebFetchInput)
	if !ok {
		// Accept string as shorthand for URL.
		if urlStr, isStr := input.(string); isStr {
			in = WebFetchInput{URL: urlStr}
		} else {
			return Result{}, fmt.Errorf("webfetch requires a URL string or {url, timeout_ms} object")
		}
	}

	url := strings.TrimSpace(in.URL)
	if url == "" {
		return Result{}, fmt.Errorf("webfetch: url is required")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}

	timeout := 15000
	if in.Timeout > 0 && in.Timeout < 60000 {
		timeout = in.Timeout
	}

	client := &http.Client{Timeout: time.Duration(timeout) * time.Millisecond}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, fmt.Errorf("webfetch: %w", err)
	}
	req.Header.Set("User-Agent", "avatars/1.0 (agent-native CLI)")

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("webfetch: %w", err)
	}
	defer resp.Body.Close()

	// Limit response to 512KB to prevent memory issues.
	limited := io.LimitReader(resp.Body, 512*1024)
	body, err := io.ReadAll(limited)
	if err != nil {
		return Result{}, fmt.Errorf("webfetch: read body: %w", err)
	}

	content := string(body)
	if len(content) > 500000 {
		content = content[:500000] + "\n... [truncated at 500KB]"
	}

	return Result{Content: content}, nil
}
