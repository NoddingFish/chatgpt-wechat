package coze

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestChatMessagesStreamHandleNormalizesRootMessage(t *testing.T) {
	api := &API{}
	response := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader("event: conversation.message.delta\n" +
			"data: {\"id\":\"message-1\",\"role\":\"assistant\",\"type\":\"answer\",\"content\":\"hello\",\"content_type\":\"text\"}\n\n" +
			"data: [DONE]\n\n")),
	}
	stream := make(chan ChatMessageStreamChannelResponse)
	go api.chatMessagesStreamHandle(context.Background(), response, stream)

	result, ok := <-stream
	if !ok {
		t.Fatal("stream closed without a response")
	}
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.Data == nil || result.Data.Content != "hello" || result.Data.Role != "assistant" {
		t.Fatalf("root message was not normalized: %#v", result.Data)
	}
}
