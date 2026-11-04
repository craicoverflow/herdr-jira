package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// mcpEndpoint is a var, not a const, so tests can point it at an httptest
// server for the duration of a test.
var mcpEndpoint = oauthIssuer + "/v1/mcp"

// errSignedOut means there's no usable refresh token (never signed in, or
// the refresh token was revoked/rejected). Callers should tell the user to
// sign in again.
var errSignedOut = errors.New("not signed in: open the Jira popup to sign in")

// mcpResponse is the JSON-RPC envelope carried inside the SSE-framed body
// (confirmed in the Task 0a spike: POST /v1/mcp returns
// Content-Type: text/event-stream with one `data: {...}` JSON-RPC message).
type mcpResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// toolResult is the shape of a tools/call result: content is a list of
// typed parts, and for every tool used here that's a single text part
// whose Text is itself a JSON string (confirmed in Task 0a/0c) that must be
// unmarshalled again.
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// mcpUnauthorized is returned by rawMCPCall for an HTTP 401, so callers can
// refresh the access token and retry once.
type mcpUnauthorized struct{ body string }

func (e *mcpUnauthorized) Error() string { return "mcp: unauthorized: " + e.body }

// rawMCPCall sends one JSON-RPC request to the MCP endpoint and decodes the
// envelope. sessionID may be empty (e.g. for "initialize"); the response's
// session id (if any) is returned so the caller can reuse it.
func rawMCPCall(ctx context.Context, client *http.Client, accessToken, sessionID, method string, params any) (json.RawMessage, string, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("mcp %s: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		return nil, "", &mcpUnauthorized{body: buf.String()}
	}

	newSessionID := resp.Header.Get("Mcp-Session-Id")
	envelope, err := readMCPEnvelope(resp)
	if err != nil {
		return nil, newSessionID, fmt.Errorf("mcp %s: %w", method, err)
	}
	if envelope.Error != nil {
		return nil, newSessionID, fmt.Errorf("mcp %s: %s (code %d)", method, envelope.Error.Message, envelope.Error.Code)
	}
	return envelope.Result, newSessionID, nil
}

// readMCPEnvelope decodes the JSON-RPC envelope from either a plain JSON
// body or an SSE-framed one ("event: message\ndata: {...}"), since the live
// endpoint uses SSE framing for a streamable-HTTP response that still
// carries exactly one message for these calls.
func readMCPEnvelope(resp *http.Response) (mcpResponse, error) {
	ctype := resp.Header.Get("Content-Type")
	if strings.Contains(ctype, "text/event-stream") {
		return decodeSSEEnvelope(resp.Body)
	}
	var out mcpResponse
	dec := json.NewDecoder(resp.Body)
	err := dec.Decode(&out)
	return out, err
}

func decodeSSEEnvelope(body interface{ Read([]byte) (int, error) }) (mcpResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			data.WriteString(strings.TrimPrefix(rest, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return mcpResponse{}, err
	}
	if data.Len() == 0 {
		return mcpResponse{}, fmt.Errorf("no data in SSE response")
	}
	var out mcpResponse
	if err := json.Unmarshal([]byte(data.String()), &out); err != nil {
		return mcpResponse{}, fmt.Errorf("decode SSE data: %w", err)
	}
	return out, nil
}

// checkToolResult reports whether a tools/call result succeeded, without
// requiring its content to be parseable JSON (some mutations, like
// transitionJiraIssue, return plain confirmation text or nothing at all).
func checkToolResult(raw json.RawMessage) error {
	var tr toolResult
	if err := json.Unmarshal(raw, &tr); err != nil {
		return fmt.Errorf("decode tool result: %w", err)
	}
	if tr.IsError {
		if len(tr.Content) > 0 {
			return fmt.Errorf("tool call failed: %s", tr.Content[0].Text)
		}
		return fmt.Errorf("tool call failed")
	}
	return nil
}

// decodeToolResultText unmarshals a tools/call result's inner text payload
// (itself a JSON string, not an object) into out.
func decodeToolResultText(raw json.RawMessage, out any) error {
	var tr toolResult
	if err := json.Unmarshal(raw, &tr); err != nil {
		return fmt.Errorf("decode tool result: %w", err)
	}
	if tr.IsError {
		if len(tr.Content) > 0 {
			return fmt.Errorf("tool call failed: %s", tr.Content[0].Text)
		}
		return fmt.Errorf("tool call failed")
	}
	if len(tr.Content) == 0 || tr.Content[0].Type != "text" {
		return fmt.Errorf("tool result has no text content")
	}
	return json.Unmarshal([]byte(tr.Content[0].Text), out)
}

// validAccessToken returns a usable access token, refreshing it first if
// it's missing, expired, or close to expiring. Refresh happens under
// withCredentialsLock so a rotated refresh token from a concurrent `tick`
// is never lost (confirmed required by the Task 0a spike's rotation
// finding).
func validAccessToken(ctx context.Context, client *http.Client) (string, error) {
	creds, err := loadCredentials()
	if err != nil {
		if errors.Is(err, errNoCredentials) {
			return "", errSignedOut
		}
		return "", err
	}
	if creds.AccessToken != "" && time.Until(creds.ExpiresAt) > 30*time.Second {
		return creds.AccessToken, nil
	}
	return refreshAndSave(ctx, client)
}

// refreshAndSave refreshes the access token under the credential store's
// lock and returns the new access token.
func refreshAndSave(ctx context.Context, client *http.Client) (string, error) {
	var accessToken string
	err := withCredentialsLock(func(creds credentials, loadErr error) (credentials, error) {
		if loadErr != nil {
			return credentials{}, errSignedOut
		}
		if creds.RefreshToken == "" {
			return credentials{}, errSignedOut
		}
		// Another process may have already refreshed while we waited for
		// the lock; use its token if it's still fresh enough.
		if creds.AccessToken != "" && time.Until(creds.ExpiresAt) > 30*time.Second {
			accessToken = creds.AccessToken
			return creds, nil
		}
		tok, err := refreshAccessToken(ctx, client, creds.ClientID, creds.RefreshToken)
		if err != nil {
			return credentials{}, fmt.Errorf("%w: %v", errSignedOut, err)
		}
		next := tokenResponseToCredentials(creds.ClientID, tok)
		accessToken = next.AccessToken
		return next, nil
	})
	if err != nil {
		if errors.Is(err, errSignedOut) {
			return "", errSignedOut
		}
		return "", err
	}
	return accessToken, nil
}

// callTool runs an MCP "initialize" then one "tools/call" for name/args,
// refreshing the access token and retrying once on a 401.
func callTool(ctx context.Context, client *http.Client, name string, args map[string]any) (json.RawMessage, error) {
	for attempt := 0; attempt < 2; attempt++ {
		accessToken, err := validAccessToken(ctx, client)
		if err != nil {
			return nil, err
		}

		_, sessionID, err := rawMCPCall(ctx, client, accessToken, "", "initialize", map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "herdr-jira", "version": "0.1"},
		})
		if err == nil {
			var result json.RawMessage
			result, _, err = rawMCPCall(ctx, client, accessToken, sessionID, "tools/call", map[string]any{
				"name":      name,
				"arguments": args,
			})
			if err == nil {
				return result, nil
			}
		}

		var unauth *mcpUnauthorized
		if errors.As(err, &unauth) && attempt == 0 {
			if _, rerr := refreshAndSave(ctx, client); rerr != nil {
				return nil, rerr
			}
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("callTool: exhausted retries")
}
