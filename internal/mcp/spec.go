package mcp

import (
	"net/url"
	"strings"
	"time"
)

const ProtocolVersion = "2024-11-05"

// ServerSpec describes how to reach one MCP server.
type ServerSpec struct {
	Name        string
	Transport   string
	Command     string
	Args        []string
	Env         map[string]string
	URL         string
	Headers     map[string]string
	Timeout     time.Duration
	Description string
}

// HTTPSpec is a convenience constructor for URL-only HTTP servers.
func HTTPSpec(rawURL string) ServerSpec {
	trimmed := strings.TrimSpace(rawURL)
	return ServerSpec{Name: trimmed, URL: trimmed, Transport: "http"}
}

func (s ServerSpec) Key() string {
	if name := strings.TrimSpace(s.Name); name != "" {
		return name
	}
	if rawURL := strings.TrimSpace(s.URL); rawURL != "" {
		return rawURL
	}
	return strings.TrimSpace(s.Command)
}

func (s ServerSpec) Endpoint() string {
	if rawURL := strings.TrimSpace(s.URL); rawURL != "" {
		return rawURL
	}
	if s.ResolvedTransport() == "stdio" {
		cmd := strings.TrimSpace(s.Command)
		if cmd == "" {
			return "stdio"
		}
		return "stdio:" + cmd
	}
	return s.Key()
}

func (s ServerSpec) ResolvedTransport() string {
	trimmed := strings.ToLower(strings.TrimSpace(s.Transport))
	if trimmed == "https" {
		return "http"
	}
	if trimmed == "http" || trimmed == "stdio" {
		return trimmed
	}
	if strings.TrimSpace(s.Command) != "" {
		return "stdio"
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(s.URL)), "stdio:") {
		return "stdio"
	}
	return "http"
}

func (s ServerSpec) Configured() bool {
	switch s.ResolvedTransport() {
	case "stdio":
		return strings.TrimSpace(s.Command) != "" || strings.HasPrefix(strings.ToLower(strings.TrimSpace(s.URL)), "stdio:")
	default:
		rawURL := strings.TrimSpace(s.URL)
		if rawURL == "" {
			return false
		}
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return false
		}
		return parsed.Scheme == "http" || parsed.Scheme == "https"
	}
}
