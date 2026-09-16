package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

type Config struct {
	Timeout time.Duration
}

// StdioConn is a bidirectional stdio transport for one MCP server process.
type StdioConn struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Wait   func() error
}

type Client struct {
	httpClient HTTPDoer
	timeout    time.Duration
	DialStdio  func(ctx context.Context, spec ServerSpec) (StdioConn, error)

	mu       sync.Mutex
	sessions map[string]*session
}

type CallResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type requestEnvelope struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type session struct {
	spec      ServerSpec
	sessionID string

	mu          sync.Mutex
	initialized bool
	stdio       *stdioSession
}

type stdioSession struct {
	stdin  io.WriteCloser
	stdout *bufio.Reader
	wait   func() error
	cancel context.CancelFunc
}

func NewClient(config Config, httpClient HTTPDoer) *Client {
	if config.Timeout <= 0 {
		config.Timeout = 10 * time.Second
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: config.Timeout}
	}
	return &Client{
		httpClient: httpClient,
		timeout:    config.Timeout,
		sessions:   make(map[string]*session),
		DialStdio:  defaultDialStdio,
	}
}

func (c *Client) Call(ctx context.Context, spec ServerSpec, method string, params any) (CallResponse, error) {
	if c == nil {
		return CallResponse{}, errors.New("mcp client is nil")
	}
	if strings.TrimSpace(method) == "" {
		return CallResponse{}, errors.New("mcp method cannot be empty")
	}
	if strings.TrimSpace(spec.URL) == "" && spec.ResolvedTransport() != "stdio" && strings.TrimSpace(spec.Command) == "" {
		return CallResponse{}, errors.New("mcp server spec is empty")
	}

	ctx, cancel := c.withTimeout(ctx, spec)
	defer cancel()

	sess, err := c.ensureSession(spec)
	if err != nil {
		return CallResponse{}, err
	}
	if err := sess.connect(ctx, c); err != nil {
		return CallResponse{}, err
	}
	return sess.call(ctx, c, method, params, false)
}

func (c *Client) Connect(ctx context.Context, spec ServerSpec) error {
	if c == nil {
		return errors.New("mcp client is nil")
	}
	ctx, cancel := c.withTimeout(ctx, spec)
	defer cancel()
	sess, err := c.ensureSession(spec)
	if err != nil {
		return err
	}
	return sess.connect(ctx, c)
}

func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	sessions := make([]*session, 0, len(c.sessions))
	for _, sess := range c.sessions {
		sessions = append(sessions, sess)
	}
	c.sessions = make(map[string]*session)
	c.mu.Unlock()

	var errs []error
	for _, sess := range sessions {
		if err := sess.close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Client) withTimeout(ctx context.Context, spec ServerSpec) (context.Context, context.CancelFunc) {
	timeout := c.timeout
	if spec.Timeout > 0 {
		timeout = spec.Timeout
	}
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func (c *Client) ensureSession(spec ServerSpec) (*session, error) {
	key := spec.Key()
	if key == "" {
		return nil, errors.New("mcp server spec requires a name, url, or command")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessions == nil {
		c.sessions = make(map[string]*session)
	}
	if sess, ok := c.sessions[key]; ok {
		return sess, nil
	}
	sess := &session{spec: spec}
	c.sessions[key] = sess
	return sess, nil
}

func (s *session) connect(ctx context.Context, client *Client) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initialized {
		return nil
	}
	if s.spec.ResolvedTransport() == "stdio" {
		if err := s.startStdioLocked(ctx, client); err != nil {
			return err
		}
	}
	response, err := s.callLocked(ctx, client, "initialize", initializeParams(), false)
	if err != nil {
		return fmt.Errorf("mcp initialize: %w", err)
	}
	if response.Error != nil && response.Error.Code != -32601 {
		return fmt.Errorf("mcp initialize failed (%d): %s", response.Error.Code, response.Error.Message)
	}
	_, _ = s.callLocked(ctx, client, "notifications/initialized", map[string]any{}, true)
	s.initialized = true
	return nil
}

func (s *session) call(ctx context.Context, client *Client, method string, params any, notification bool) (CallResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callLocked(ctx, client, method, params, notification)
}

func (s *session) callLocked(ctx context.Context, client *Client, method string, params any, notification bool) (CallResponse, error) {
	if s.spec.ResolvedTransport() == "stdio" {
		return s.callStdioLocked(ctx, method, params, notification)
	}
	return s.callHTTPLocked(ctx, client, method, params, notification)
}

func (s *session) callHTTPLocked(ctx context.Context, client *Client, method string, params any, notification bool) (CallResponse, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(s.spec.URL))
	if err != nil {
		return CallResponse{}, fmt.Errorf("parse mcp url: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return CallResponse{}, fmt.Errorf("unsupported mcp url scheme %q (supported: http, https, stdio)", parsedURL.Scheme)
	}
	if parsedURL.Host == "" {
		return CallResponse{}, errors.New("mcp url must include host")
	}

	envelope := requestEnvelope{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	if !notification {
		envelope.ID = fmt.Sprintf("mcp-%d", time.Now().UTC().UnixNano())
	}
	requestBody, err := json.Marshal(envelope)
	if err != nil {
		return CallResponse{}, fmt.Errorf("marshal mcp request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(requestBody))
	if err != nil {
		return CallResponse{}, fmt.Errorf("build mcp request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "avatars/mcp-client")
	if s.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", s.sessionID)
	}
	for key, value := range s.spec.Headers {
		if strings.TrimSpace(key) == "" {
			continue
		}
		req.Header.Set(key, value)
	}

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return CallResponse{}, fmt.Errorf("send mcp request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if sessionID := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id")); sessionID != "" {
		s.sessionID = sessionID
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return CallResponse{}, fmt.Errorf("read mcp response: %w", err)
	}
	if notification {
		if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusAccepted || len(bytes.TrimSpace(body)) == 0 {
			return CallResponse{JSONRPC: "2.0"}, nil
		}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return CallResponse{}, fmt.Errorf("mcp server returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if notification && len(bytes.TrimSpace(body)) == 0 {
		return CallResponse{JSONRPC: "2.0"}, nil
	}

	decoded, err := decodeCallResponse(body)
	if err != nil {
		if notification {
			return CallResponse{JSONRPC: "2.0"}, nil
		}
		return CallResponse{}, err
	}
	return decoded, nil
}

func (s *session) startStdioLocked(ctx context.Context, client *Client) error {
	if s.stdio != nil {
		return nil
	}
	dial := client.DialStdio
	if dial == nil {
		dial = defaultDialStdio
	}
	conn, err := dial(ctx, s.spec)
	if err != nil {
		return err
	}
	if conn.Stdin == nil || conn.Stdout == nil {
		return errors.New("stdio: connection is missing stdin or stdout")
	}
	s.stdio = &stdioSession{
		stdin:  conn.Stdin,
		stdout: bufio.NewReader(conn.Stdout),
		wait:   conn.Wait,
	}
	return nil
}

func (s *session) callStdioLocked(ctx context.Context, method string, params any, notification bool) (CallResponse, error) {
	if s.stdio == nil {
		return CallResponse{}, errors.New("stdio: session is not started")
	}
	envelope := requestEnvelope{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	if !notification {
		envelope.ID = fmt.Sprintf("mcp-stdio-%d", time.Now().UTC().UnixNano())
	}
	requestBody, err := json.Marshal(envelope)
	if err != nil {
		return CallResponse{}, fmt.Errorf("marshal stdio request: %w", err)
	}
	requestBody = append(requestBody, '\n')
	if _, err := s.stdio.stdin.Write(requestBody); err != nil {
		return CallResponse{}, fmt.Errorf("write stdio request: %w", err)
	}
	if notification {
		return CallResponse{JSONRPC: "2.0"}, nil
	}
	line, err := readStdioLine(ctx, s.stdio.stdout)
	if err != nil {
		return CallResponse{}, err
	}
	decoded, err := decodeCallResponse(line)
	if err != nil {
		return CallResponse{}, err
	}
	return decoded, nil
}

func (s *session) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stdio == nil {
		s.initialized = false
		return nil
	}
	_ = s.stdio.stdin.Close()
	if s.stdio.cancel != nil {
		s.stdio.cancel()
	}
	var waitErr error
	if s.stdio.wait != nil {
		waitErr = s.stdio.wait()
	}
	s.stdio = nil
	s.initialized = false
	return waitErr
}

func defaultDialStdio(ctx context.Context, spec ServerSpec) (StdioConn, error) {
	cmdName, args, err := stdioCommand(spec)
	if err != nil {
		return StdioConn{}, err
	}
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, cmdName, args...)
	if len(spec.Env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range spec.Env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return StdioConn{}, fmt.Errorf("stdio: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return StdioConn{}, fmt.Errorf("stdio: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return StdioConn{}, fmt.Errorf("stdio: start command: %w", err)
	}
	return StdioConn{
		Stdin:  stdin,
		Stdout: stdout,
		Wait: func() error {
			cancel()
			return cmd.Wait()
		},
	}, nil
}

func stdioCommand(spec ServerSpec) (string, []string, error) {
	if cmd := strings.TrimSpace(spec.Command); cmd != "" {
		return cmd, append([]string(nil), spec.Args...), nil
	}
	rawURL := strings.TrimSpace(spec.URL)
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", nil, fmt.Errorf("stdio: parse url: %w", err)
	}
	combined := strings.TrimSpace(parsedURL.Host)
	if path := strings.TrimPrefix(parsedURL.Path, "/"); path != "" {
		if combined == "" {
			combined = path
		} else {
			combined = combined + " " + strings.ReplaceAll(path, "/", " ")
		}
	}
	if query := strings.TrimSpace(parsedURL.RawQuery); query != "" {
		combined = strings.TrimSpace(combined + " " + query)
	}
	parts := strings.Fields(combined)
	if len(parts) == 0 {
		return "", nil, errors.New("stdio: command is required")
	}
	return parts[0], parts[1:], nil
}

func initializeParams() map[string]any {
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "avatars", "version": "0.1.0"},
	}
}

func readStdioLine(ctx context.Context, reader *bufio.Reader) ([]byte, error) {
	type readResult struct {
		line []byte
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		line, err := reader.ReadBytes('\n')
		ch <- readResult{line: bytes.TrimSpace(line), err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("read stdio response: %w", ctx.Err())
	case result := <-ch:
		if result.err != nil && result.err != io.EOF {
			return nil, fmt.Errorf("read stdio response: %w", result.err)
		}
		if len(result.line) == 0 {
			if result.err == io.EOF {
				return nil, errors.New("stdio: empty response from server")
			}
			return nil, errors.New("stdio: empty response line from server")
		}
		return result.line, nil
	}
}

func decodeCallResponse(body []byte) (CallResponse, error) {
	var raw struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *RPCError       `json:"error,omitempty"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return CallResponse{}, fmt.Errorf("decode mcp response: %w", err)
	}
	decoded := CallResponse{
		JSONRPC: raw.JSONRPC,
		Result:  raw.Result,
		Error:   raw.Error,
	}
	if decoded.JSONRPC == "" {
		decoded.JSONRPC = "2.0"
	}
	if len(raw.ID) > 0 && string(raw.ID) != "null" {
		var asString string
		if err := json.Unmarshal(raw.ID, &asString); err == nil {
			decoded.ID = asString
		} else {
			decoded.ID = strings.TrimSpace(string(raw.ID))
		}
	}
	return decoded, nil
}
