// Package mcp implements an MCP (Model Context Protocol) server for Cortex.
// This enables cross-tool access to Cortex context from any MCP-compatible client
// (Claude Code, Cursor, Copilot, etc.).
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
)

// Server implements the MCP protocol over stdio (JSON-RPC 2.0).
type Server struct {
	cfg   *config.Config
	store *storage.Storage
}

// NewServer creates a new MCP server.
func NewServer(cfg *config.Config, store *storage.Storage) *Server {
	return &Server{
		cfg:   cfg,
		store: store,
	}
}

// Tool describes an MCP tool.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Request represents a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response represents a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC 2.0 error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// tools returns the list of tools exposed by this server.
func (s *Server) tools() []Tool {
	return []Tool{
		{
			Name:        "cortex_search",
			Description: "Search Cortex context memory for relevant insights, decisions, and patterns",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {
						"type": "string",
						"description": "The search query"
					},
					"limit": {
						"type": "integer",
						"description": "Maximum number of results (default: 10)"
					}
				},
				"required": ["query"]
			}`),
		},
		{
			Name:        "cortex_recall",
			Description: "Detailed recall of what Cortex knows about a topic, including insights and entity relationships",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"topic": {
						"type": "string",
						"description": "The topic to recall"
					}
				},
				"required": ["topic"]
			}`),
		},
		{
			Name:        "cortex_record",
			Description: "Record a decision, correction, or insight in Cortex",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"type": {
						"type": "string",
						"enum": ["decision", "correction", "insight"],
						"description": "Type of context to record"
					},
					"content": {
						"type": "string",
						"description": "The content to record"
					}
				},
				"required": ["type", "content"]
			}`),
		},
	}
}

// Serve runs the MCP server over stdio, reading JSON-RPC requests from stdin
// and writing responses to stdout.
func (s *Server) Serve(ctx context.Context) error {
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var req Request
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			continue
		}

		resp := s.handleRequest(&req)
		if err := encoder.Encode(resp); err != nil {
			return fmt.Errorf("failed to encode response: %w", err)
		}
	}
}

func (s *Server) handleRequest(req *Request) *Response {
	switch req.Method {
	case "initialize":
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]interface{}{
					"tools": map[string]interface{}{},
				},
				"serverInfo": map[string]interface{}{
					"name":    "cortex",
					"version": "0.2.0",
				},
			},
		}

	case "tools/list":
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"tools": s.tools(),
			},
		}

	case "tools/call":
		return s.handleToolCall(req)

	default:
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &RPCError{
				Code:    -32601,
				Message: fmt.Sprintf("method not found: %s", req.Method),
			},
		}
	}
}

func (s *Server) handleToolCall(req *Request) *Response {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: -32602, Message: "invalid params"},
		}
	}

	var result string
	var err error

	switch params.Name {
	case "cortex_search":
		result, err = s.toolSearch(params.Arguments)
	case "cortex_recall":
		result, err = s.toolRecall(params.Arguments)
	case "cortex_record":
		result, err = s.toolRecord(params.Arguments)
	default:
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: -32602, Message: fmt.Sprintf("unknown tool: %s", params.Name)},
		}
	}

	if err != nil {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": fmt.Sprintf("Error: %v", err)},
				},
				"isError": true,
			},
		}
	}

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": result},
			},
		},
	}
}

func (s *Server) toolSearch(args json.RawMessage) (string, error) {
	var input struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if input.Limit <= 0 {
		input.Limit = 10
	}

	results, err := s.store.SearchInsights(input.Query, input.Limit)
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}

	if len(results) == 0 {
		return "No results found.", nil
	}

	output, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func (s *Server) toolRecall(args json.RawMessage) (string, error) {
	var input struct {
		Topic string `json:"topic"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	// Search insights and events for the topic
	insights, err := s.store.SearchInsights(input.Topic, 20)
	if err != nil {
		return "", fmt.Errorf("recall failed: %w", err)
	}

	if len(insights) == 0 {
		return fmt.Sprintf("No context found for topic: %s", input.Topic), nil
	}

	output, err := json.MarshalIndent(insights, "", "  ")
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func (s *Server) toolRecord(args json.RawMessage) (string, error) {
	var input struct {
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	err := s.store.StoreInsight("mcp", input.Type, input.Content, 5, nil, "recorded via MCP")
	if err != nil {
		return "", fmt.Errorf("failed to record: %w", err)
	}

	return fmt.Sprintf("Recorded %s: %s", input.Type, input.Content), nil
}
