package popo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGatewayClientHealthAndOutbound(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/webhook/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/outbound":
			gotAuth = r.Header.Get("Authorization")
			data, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(data, &gotBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"attempted"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := &GatewayClient{BaseURL: srv.URL, Token: "secret"}
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
	if err := c.Send(context.Background(), OutboundRequest{
		Content: "hello",
		Channel: dj01botChannelPopo,
		ChatID:  "yujian01@corp.netease.com",
		RobotID: "default",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotBody["channel"] != dj01botChannelPopo || gotBody["content"] != "hello" {
		t.Fatalf("body = %+v", gotBody)
	}
}

func TestGatewayClientRejectsIncompleteOutbound(t *testing.T) {
	c := &GatewayClient{BaseURL: "http://127.0.0.1:9"}
	if err := c.Send(context.Background(), OutboundRequest{Content: "x", Channel: dj01botChannelPopo}); err == nil {
		t.Fatal("expected missing chat_id error")
	}
}
