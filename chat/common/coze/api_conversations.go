package coze

import (
	"context"
	"errors"
	"net/http"
)

// ConversationRequest represents the request for creating a conversation
type ConversationRequest struct {
	BotID string `json:"bot_id"`
}

// ConversationResponse represents the response from creating a conversation
type ConversationResponse struct {
	Code    int    `json:"code"`
	Message string `json:"msg"`
	Data    struct {
		ID            string `json:"id"`
		CreatedAt     int64  `json:"created_at"`
		MetaData      string `json:"meta_data"`
		LastSectionID string `json:"last_section_id"`
	} `json:"data"`
}

// CreateConversation creates a new conversation
func (api *API) CreateConversation(ctx context.Context, req *ConversationRequest) (resp *ConversationResponse, err error) {
	if req.BotID == "" {
		err = errors.New("ConversationRequest.BotID is required")
		return
	}

	httpReq, err := api.createBaseRequest(ctx, http.MethodPost, "/v1/conversation/create", req)
	if err != nil {
		return
	}
	err = api.c.sendJSONRequest(httpReq, &resp)
	return
}

// ListConversationsRequest represents the request for listing conversations
type ListConversationsRequest struct {
	BotID    string `json:"bot_id"`
	PageNum  int    `json:"page_num"`
	PageSize int    `json:"page_size"`
}

// ListConversationsResponse represents the response from listing conversations
type ListConversationsResponse struct {
	Code    int    `json:"code"`
	Message string `json:"msg"`
	Data    struct {
		Conversations []struct {
			ID            string `json:"id"`
			CreatedAt     int64  `json:"created_at"`
			LastSectionID string `json:"last_section_id"`
		} `json:"conversations"`
		HasMore bool `json:"has_more"`
	} `json:"data"`
}

// ListConversations lists conversations for a bot
func (api *API) ListConversations(ctx context.Context, req *ListConversationsRequest) (resp *ListConversationsResponse, err error) {
	if req.BotID == "" {
		err = errors.New("ListConversationsRequest.BotID is required")
		return
	}

	httpReq, err := api.createBaseRequest(ctx, http.MethodPost, "/v1/conversation/list", req)
	if err != nil {
		return
	}
	err = api.c.sendJSONRequest(httpReq, &resp)
	return
}
