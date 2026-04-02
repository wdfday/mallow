package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const appName = "strategist"

// StrategistClient communicates with the Strategist service.
type StrategistClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewStrategistClient creates a new client for the strategist service.
func NewStrategistClient(baseURL string) *StrategistClient {
	return &StrategistClient{
		BaseURL:    baseURL,
		HTTPClient: http.DefaultClient,
	}
}

// Chat sends a user message to the strategist and returns the final text reply.
// It delegates session management to the strategist service itself.
func (s *StrategistClient) Chat(userID, message string) (reply string, sessionID string, err error) {
	body, _ := json.Marshal(map[string]any{
		"user_id": userID,
		"message": message,
	})

	resp, err := s.HTTPClient.Post(s.BaseURL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("status %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		Data struct {
			Reply     string `json:"reply"`
			SessionID string `json:"session_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", "", fmt.Errorf("parse response: %w", err)
	}

	return result.Data.Reply, result.Data.SessionID, nil
}

// ChatStream sends a message and returns a streaming SSE response body via the ADK run/sse endpoint.
// The caller is responsible for closing the returned ReadCloser.
func (s *StrategistClient) ChatStream(userID, message string) (body io.ReadCloser, sessionID string, err error) {
	sessionID = fmt.Sprintf("session_%s", userID)

	reqBody, _ := json.Marshal(map[string]any{
		"appName":   appName,
		"userId":    userID,
		"sessionId": sessionID,
		"streaming": true,
		"newMessage": map[string]any{
			"role": "user",
			"parts": []map[string]any{
				{"text": message},
			},
		},
	})

	url := s.BaseURL + "/api/run/sse"
	resp, err := s.HTTPClient.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, sessionID, fmt.Errorf("run/sse: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(resp.Body)
		return nil, sessionID, fmt.Errorf("run/sse: status %d: %s", resp.StatusCode, errBody)
	}

	return resp.Body, sessionID, nil
}
