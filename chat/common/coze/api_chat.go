package coze

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ChatMessageRequest represents the request for Coze chat API v3
// Reference: https://www.coze.cn/docs/developer_guides/chat_v3
type ChatMessageRequest struct {
	BotID           string                 `json:"bot_id"`           // Required
	User            string                 `json:"user_id"`          // Required in V3
	Stream          *bool                  `json:"stream,omitempty"` // Optional, default false
	ConversationID  string                 `json:"conversation_id,omitempty"`
	Messages        []ChatMessage          `json:"additional_messages,omitempty"` // V3 uses additional_messages
	AutoSaveHistory *bool                  `json:"auto_save_history,omitempty"`   // Optional, default true
	MetaData        map[string]interface{} `json:"meta_data,omitempty"`
	CustomVariables map[string]interface{} `json:"custom_variables,omitempty"`
}

// ChatMessage represents a single message in the conversation
type ChatMessage struct {
	Role        string                 `json:"role"` // user, assistant, system
	Content     string                 `json:"content"`
	ContentType string                 `json:"content_type"`        // text, object_string - V3 required
	Type        string                 `json:"type,omitempty"`      // question, answer - V3 需要此字段来正确识别消息类型
	MetaData    map[string]interface{} `json:"meta_data,omitempty"` // V3 可选元数据
}

// ChatMessageResponse represents the response from Coze chat API v3
type ChatMessageResponse struct {
	Code    int    `json:"code"`
	Message string `json:"msg"`
	Data    struct {
		ID             string `json:"id"`
		ConversationID string `json:"conversation_id"`
		BotID          string `json:"bot_id"`
		CreatedAt      int64  `json:"created_at"`
		CompletedAt    int64  `json:"completed_at,omitempty"`
		FailedAt       int64  `json:"failed_at,omitempty"`
		LastError      struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"last_error"`
		Status    string `json:"status"` // created, in_progress, completed, failed
		Usage     Usage  `json:"usage"`
		SectionID string `json:"section_id,omitempty"`
	} `json:"data"`
}

// MessageListRequest represents request for retrieving messages
type MessageListRequest struct {
	ConversationID string `json:"conversation_id"`
	BotID          string `json:"bot_id"`
}

// MessageObject represents a message in the conversation
type MessageObject struct {
	ID               string      `json:"id"`
	Role             string      `json:"role"`                        // user, assistant
	Content          interface{} `json:"content"`                     // 可能是字符串或对象
	ContentType      string      `json:"content_type"`                // text, object_string
	Type             string      `json:"type"`                        // question, answer, verbose
	CreatedAt        float64     `json:"created_at"`                  // Coze V3 使用浮点数时间戳
	UpdatedAt        float64     `json:"updated_at"`                  // Coze V3 使用浮点数时间戳
	BotID            string      `json:"bot_id"`                      // Bot ID
	ChatID           string      `json:"chat_id"`                     // Chat ID
	ConversationID   string      `json:"conversation_id"`             // Conversation ID
	ReasoningContent string      `json:"reasoning_content,omitempty"` // 推理内容
}

// MessageListResponse represents response from message list API
// 注意：Coze API 可能返回不同的格式，使用 interface{} 来兼容
type MessageListResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"msg"`
	Data    interface{} `json:"data"` // 可能是对象或数组
}

// GetMessageListData 解析 Data 字段为结构化数据
func (r *MessageListResponse) GetMessageListData() (*MessageListData, error) {
	if r.Data == nil {
		return nil, fmt.Errorf("data is nil")
	}

	// 尝试将 data 转换为 JSON 并重新解析
	dataBytes, err := json.Marshal(r.Data)
	if err != nil {
		return nil, err
	}

	// 首先尝试解析为 MessageListData 结构（对象格式）
	var result MessageListData
	err = json.Unmarshal(dataBytes, &result)
	if err == nil && result.Items != nil {
		// 成功解析为对象格式
		return &result, nil
	}

	// 如果失败，尝试解析为数组格式（直接的消息列表）
	var messageArray []MessageObject
	err = json.Unmarshal(dataBytes, &messageArray)
	if err == nil {
		// 成功解析为数组格式，转换为 MessageListData
		return &MessageListData{
			HasMore: false,
			Items:   messageArray,
		}, nil
	}

	// 两种格式都失败，返回错误
	return nil, fmt.Errorf("failed to parse data as object or array: %v", err)
}

// MessageListData 表示消息列表的实际数据结构
type MessageListData struct {
	HasMore bool            `json:"has_more"`
	Items   []MessageObject `json:"items"`
}

// GetTextContent 从 Content 字段中提取文本内容
// Content 可能是字符串或 JSON 对象
func (m *MessageObject) GetTextContent() string {
	if m.Content == nil {
		return ""
	}

	// 如果已经是字符串，直接返回
	if str, ok := m.Content.(string); ok {
		return str
	}

	// 如果是 map，尝试提取其中的文本
	if objMap, ok := m.Content.(map[string]interface{}); ok {
		// 检查是否有 msg_type 和 data 字段（Coze 的 verbose 消息格式）
		if msgType, exists := objMap["msg_type"]; exists {
			if msgTypeStr, ok := msgType.(string); ok {
				// 对于 generate_answer_finish 等类型，返回空或特殊标记
				if msgTypeStr == "generate_answer_finish" || msgTypeStr == "empty result" {
					return ""
				}
			}
		}
		// 如果有 data 字段，尝试返回
		if data, exists := objMap["data"]; exists {
			if dataStr, ok := data.(string); ok && dataStr != "" {
				return dataStr
			}
		}
	}

	// 其他情况，转换为 JSON 字符串
	if bytes, err := json.Marshal(m.Content); err == nil {
		return string(bytes)
	}

	return ""
}

// Usage represents token usage
type Usage struct {
	TokenCount  int `json:"token_count"`
	OutputCount int `json:"output_count"`
	InputCount  int `json:"input_count"`
}

// ChatMessages sends a chat message to Coze v3 API and returns the response
func (api *API) ChatMessages(ctx context.Context, req *ChatMessageRequest) (resp *ChatMessageResponse, err error) {
	// Set Stream to false for non-streaming request
	falseValue := false
	req.Stream = &falseValue

	// 关键修复：如果有 conversation_id，将其作为 URL 参数传递（与 curl 示例保持一致）
	apiUrl := "/v3/chat"
	if req.ConversationID != "" {
		apiUrl = fmt.Sprintf("/v3/chat?conversation_id=%s", req.ConversationID)
	}

	// Use v3/chat endpoint for non-streaming chat
	httpReq, err := api.createBaseRequest(ctx, http.MethodPost, apiUrl, req)
	if err != nil {
		return
	}

	// Add debug logging - print full request details
	fmt.Printf("[Coze V3 API] Request URL: %s\n", httpReq.URL.String())
	fmt.Printf("[Coze V3 API] Request Method: %s\n", httpReq.Method)
	fmt.Printf("[Coze V3 API] Request Headers: Authorization=%s, Content-Type=%s\n",
		httpReq.Header.Get("Authorization"), httpReq.Header.Get("Content-Type"))

	// Print JSON body for debugging
	if reqBody, err := json.MarshalIndent(req, "", "  "); err == nil {
		fmt.Printf("[Coze V3 API] Request JSON Body:\n%s\n", string(reqBody))
	} else {
		fmt.Printf("[Coze V3 API] Request Body: %+v\n", req)
	}

	err = api.c.sendJSONRequest(httpReq, &resp)
	if err != nil {
		fmt.Printf("[Coze V3 API] Error: %v\n", err)
	}
	if resp != nil {
		fmt.Printf("[Coze V3 API] Response Code: %d, Message: %s\n", resp.Code, resp.Message)
		if resp.Code == 0 {
			fmt.Printf("[Coze V3 API] Full Response Data: %+v\n", resp.Data)
			fmt.Printf("[Coze V3 API] Conversation ID: %s, Status: %s\n", resp.Data.ConversationID, resp.Data.Status)
		} else {
			fmt.Printf("[Coze V3 API] Error Details - Code: %d, Msg: %s\n", resp.Data.LastError.Code, resp.Data.LastError.Msg)
		}
	}
	return
}

// GetMessageList retrieves messages from a conversation (V3 API)
func (api *API) GetMessageList(ctx context.Context, conversationID string, botID string) (resp *MessageListResponse, err error) {
	// Coze V3 API message/list 需要通过 URL 查询参数传递
	// 注意：这里应该使用 conversation_id，但某些情况下可能需要 section_id
	url := fmt.Sprintf("/v3/chat/message/list?conversation_id=%s&bot_id=%s", conversationID, botID)

	fmt.Printf("[Coze V3 GetMessageList] Request - ConversationID: %s, BotID: %s\n", conversationID, botID)
	fmt.Printf("[Coze V3 GetMessageList] Request URL: %s\n", url)

	// 创建 GET 请求，不需要请求体
	httpReq, err := api.createBaseRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}

	err = api.c.sendJSONRequest(httpReq, &resp)
	if err != nil {
		fmt.Printf("[Coze V3 GetMessageList] Error: %v\n", err)
		return
	}

	if resp != nil {
		fmt.Printf("[Coze V3 GetMessageList] Response Code: %d, Message: %s\n", resp.Code, resp.Message)
		if resp.Code == 0 {
			// 解析 Data 字段
			data, err := resp.GetMessageListData()
			if err != nil {
				fmt.Printf("[Coze V3 GetMessageList] Parse Data Error: %v\n", err)
				fmt.Printf("[Coze V3 GetMessageList] Raw Data: %+v\n", resp.Data)
			} else {
				fmt.Printf("[Coze V3 GetMessageList] Retrieved %d messages\n", len(data.Items))
				for i, msg := range data.Items {
					fmt.Printf("[Coze V3 Message %d] ID=%s, Role=%s, Type=%s, ContentType=%s, Content=%s\n",
						i, msg.ID, msg.Role, msg.Type, msg.ContentType, msg.GetTextContent())
				}
			}
		} else {
			fmt.Printf("[Coze V3 GetMessageList] Error Response: %+v\n", resp)
		}
	}
	return
}

// GetMessageListByChatID retrieves messages using chat_id (V3 API)
// This is the correct method for V3 API when you have a chat_id from the response
func (api *API) GetMessageListByChatID(ctx context.Context, chatID string, conversationID string, botID string) (resp *MessageListResponse, err error) {
	// Coze V3 API 需要使用 chat_id 来获取消息
	// 参考: https://www.coze.cn/docs/developer_guides/chat_message_list
	url := fmt.Sprintf("/v3/chat/message/list?conversation_id=%s&chat_id=%s&bot_id=%s", conversationID, chatID, botID)

	fmt.Printf("[Coze V3 GetMessageListByChatID] Request - ChatID: %s, ConversationID: %s, BotID: %s\n", chatID, conversationID, botID)
	fmt.Printf("[Coze V3 GetMessageListByChatID] Request URL: %s\n", url)

	// 创建 GET 请求，不需要请求体
	httpReq, err := api.createBaseRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}

	err = api.c.sendJSONRequest(httpReq, &resp)
	if err != nil {
		fmt.Printf("[Coze V3 GetMessageListByChatID] Error: %v\n", err)
		return
	}

	if resp != nil {
		fmt.Printf("[Coze V3 GetMessageListByChatID] Response Code: %d, Message: %s\n", resp.Code, resp.Message)
		if resp.Code == 0 {
			// 解析 Data 字段
			data, err := resp.GetMessageListData()
			if err != nil {
				fmt.Printf("[Coze V3 GetMessageListByChatID] Parse Data Error: %v\n", err)
				fmt.Printf("[Coze V3 GetMessageListByChatID] Raw Data: %+v\n", resp.Data)
			} else {
				fmt.Printf("[Coze V3 GetMessageListByChatID] Retrieved %d messages\n", len(data.Items))
				for i, msg := range data.Items {
					fmt.Printf("[Coze V3 Message %d] ID=%s, Role=%s, Type=%s, ContentType=%s, Content=%s\n",
						i, msg.ID, msg.Role, msg.Type, msg.ContentType, msg.GetTextContent())
				}
			}
		} else {
			fmt.Printf("[Coze V3 GetMessageListByChatID] Error Response: %+v\n", resp)
		}
	}
	return
}

// GetMessageListWithRetry retrieves messages with retry logic for persistence delay
func (api *API) GetMessageListWithRetry(ctx context.Context, conversationID string, botID string, maxRetries int) (resp *MessageListResponse, err error) {
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			// 等待一段时间后重试
			fmt.Printf("[Coze V3 GetMessageList] Retry %d/%d after delay...\n", i, maxRetries)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(i*500) * time.Millisecond):
				// 递增延迟：500ms, 1000ms, 1500ms...
			}
		}

		resp, err = api.GetMessageList(ctx, conversationID, botID)
		if err != nil {
			continue
		}

		// 如果成功且返回了消息，直接返回
		if resp.Code == 0 {
			data, parseErr := resp.GetMessageListData()
			if parseErr == nil && len(data.Items) > 0 {
				return resp, nil
			}
		}

		// 如果是无效聊天错误，继续重试
		if resp.Code == 4001 {
			fmt.Printf("[Coze V3 GetMessageList] Got invalid chat error, will retry...\n")
			continue
		}

		// 其他错误，直接返回
		return resp, nil
	}

	return resp, err
}
