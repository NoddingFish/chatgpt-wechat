package coze

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ChatMessageStreamResponse represents a single event in the stream (v3 API)
type ChatMessageStreamResponse struct {
	Event                      string            `json:"-"` // Set from SSE event line
	ID                         string            `json:"id,omitempty"`
	ConversationID             string            `json:"conversation_id,omitempty"`
	BotID                      string            `json:"bot_id,omitempty"`
	CreatedAt                  int64             `json:"created_at,omitempty"`
	CompletedAt                int64             `json:"completed_at,omitempty"`
	FailedAt                   int64             `json:"failed_at,omitempty"`
	Status                     string            `json:"status,omitempty"` // created, in_progress, completed, failed
	LastError                  *StreamError      `json:"last_error,omitempty"`
	Usage                      *Usage            `json:"usage,omitempty"`
	SectionID                  string            `json:"section_id,omitempty"`
	InsertedAdditionalMessages []InsertedMessage `json:"inserted_additional_messages,omitempty"`
	TimeCost                   *TimeCost         `json:"time_cost,omitempty"`
	// 关键修复：某些 Coze 事件将消息内容直接放在根级别，而不是 data 字段中
	Role        string           `json:"role,omitempty"` // user, assistant
	Type        string           `json:"type,omitempty"` // answer, verbose, follow_up, etc.
	Content     string           `json:"content,omitempty"`
	ContentType string           `json:"content_type,omitempty"` // text, object_string
	ChatID      string           `json:"chat_id,omitempty"`
	Data        *StreamEventData `json:"data,omitempty"` // For message content events (某些事件使用)
}

// InsertedMessage represents a message ID in inserted_additional_messages
type InsertedMessage struct {
	ID string `json:"id"`
}

// TimeCost represents timing information
type TimeCost struct {
	TotalDurationMs int64 `json:"total_duration_ms"`
}

// StreamError represents error information
type StreamError struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// StreamEventData represents the message data in conversation.message.delta events
type StreamEventData struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	BotID          string `json:"bot_id"`
	Role           string `json:"role"` // user, assistant
	Type           string `json:"type"` // answer, verbose, follow_up, etc.
	Content        string `json:"content"`
	ContentType    string `json:"content_type"` // text, object_string
	CreatedAt      int64  `json:"created_at"`
}

// ChatMessageStreamChannelResponse wraps the stream response with potential error
type ChatMessageStreamChannelResponse struct {
	ChatMessageStreamResponse
	Err error `json:"-"`
}

// ChatMessagesStreamRaw sends a streaming chat request and returns raw HTTP response (v3 API)
func (api *API) ChatMessagesStreamRaw(ctx context.Context, req *ChatMessageRequest) (*http.Response, error) {
	// Set Stream to true for streaming request
	trueValue := true
	req.Stream = &trueValue

	// 关键修复：如果有 conversation_id，将其作为 URL 参数传递（与 curl 示例保持一致）
	apiUrl := "/v3/chat"
	if req.ConversationID != "" {
		apiUrl = fmt.Sprintf("/v3/chat?conversation_id=%s", req.ConversationID)
	}

	httpReq, err := api.createBaseRequest(ctx, http.MethodPost, apiUrl, req)
	if err != nil {
		return nil, err
	}

	// Add debug logging
	fmt.Printf("[Coze V3 Stream] Request URL: %s\n", httpReq.URL.String())
	fmt.Printf("[Coze V3 Stream] Request Method: %s\n", httpReq.Method)

	// Print JSON body for debugging
	if reqBody, err := json.MarshalIndent(req, "", "  "); err == nil {
		fmt.Printf("[Coze V3 Stream] Request JSON Body:\n%s\n", string(reqBody))
	} else {
		fmt.Printf("[Coze V3 Stream] Request Body (error): %+v\n", req)
	}

	return api.c.sendRequest(httpReq)
}

// ChatMessagesStream sends a streaming chat request and returns a channel of responses
func (api *API) ChatMessagesStream(ctx context.Context, req *ChatMessageRequest) (chan ChatMessageStreamChannelResponse, error) {
	httpResp, err := api.ChatMessagesStreamRaw(ctx, req)
	if err != nil {
		return nil, err
	}

	streamChannel := make(chan ChatMessageStreamChannelResponse)
	go api.chatMessagesStreamHandle(ctx, httpResp, streamChannel)
	return streamChannel, nil
}

// chatMessagesStreamHandle processes the HTTP response stream
func (api *API) chatMessagesStreamHandle(ctx context.Context, resp *http.Response, streamChannel chan ChatMessageStreamChannelResponse) {
	defer close(streamChannel)
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			fmt.Println("Error closing response body:", err)
		}
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		streamChannel <- ChatMessageStreamChannelResponse{
			Err: fmt.Errorf("HTTP error %d: %s", resp.StatusCode, string(body)),
		}
		return
	}

	reader := bufio.NewReader(resp.Body)
	var currentEvent string
	for {
		select {
		case <-ctx.Done():
			return
		default:
			line, err := reader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					streamChannel <- ChatMessageStreamChannelResponse{
						Err: fmt.Errorf("read stream: %w", err),
					}
				}
				return
			}

			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			// Debug: print raw line
			fmt.Printf("[Coze Stream Raw] %s\n", line)

			// Coze V3 uses SSE format with event and data lines
			if strings.HasPrefix(line, "event:") {
				currentEvent = strings.TrimPrefix(line, "event:")
				currentEvent = strings.TrimSpace(currentEvent)
				fmt.Printf("[Coze V3 Stream] Received event: '%s'\n", currentEvent)
				continue
			}

			if strings.HasPrefix(line, "data:") {
				data := strings.TrimPrefix(line, "data:")
				data = strings.TrimSpace(data)

				// Check for [DONE] marker - it can be a plain string or quoted
				if data == "[DONE]" || data == `"[DONE]"` {
					fmt.Println("[Coze V3 Stream] Received [DONE]")
					return
				}

				// Print raw data before parsing
				fmt.Printf("[Coze V3 Stream] Raw data: %s\n", data)

				var streamResp ChatMessageStreamResponse
				if err := json.Unmarshal([]byte(data), &streamResp); err != nil {
					fmt.Printf("[Coze V3 Stream] JSON decode error: %v, data: %s\n", err, data)
					// Don't return on error, continue processing
					continue
				}

				// Set the event type from the previous event line
				streamResp.Event = currentEvent

				fmt.Printf("[Coze V3 Stream] Parsed Event: '%s'\n", streamResp.Event)
				if streamResp.Status != "" {
					fmt.Printf("[Coze V3 Stream] Status: %s\n", streamResp.Status)
				}
				if streamResp.LastError != nil && streamResp.LastError.Code != 0 {
					fmt.Printf("[Coze V3 Stream] Error: Code=%d, Msg=%s\n", streamResp.LastError.Code, streamResp.LastError.Msg)
				}
				if streamResp.Data != nil {
					fmt.Printf("[Coze V3 Stream] Data Type: '%s', Role: '%s', ContentType: '%s', Content Length: %d\n",
						streamResp.Data.Type, streamResp.Data.Role, streamResp.Data.ContentType, len(streamResp.Data.Content))
					if len(streamResp.Data.Content) < 200 {
						fmt.Printf("[Coze V3 Stream] Data Content: '%s'\n", streamResp.Data.Content)
					} else {
						fmt.Printf("[Coze V3 Stream] Data Content (first 200): '%s...'\n", streamResp.Data.Content[:200])
					}
				}
				if streamResp.ConversationID != "" {
					fmt.Printf("[Coze V3 Stream] Conversation ID: %s\n", streamResp.ConversationID)
				}
				// Log inserted_additional_messages if present
				if len(streamResp.InsertedAdditionalMessages) > 0 {
					fmt.Printf("[Coze V3 Stream] Inserted Messages: %+v\n", streamResp.InsertedAdditionalMessages)
				}

				streamChannel <- ChatMessageStreamChannelResponse{
					ChatMessageStreamResponse: streamResp,
				}
			}
		}
	}
}
