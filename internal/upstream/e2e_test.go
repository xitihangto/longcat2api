package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linquin/longcat2api/internal/account"
	"github.com/linquin/longcat2api/internal/config"
)

// fakeUpstream 用 httptest 模拟妙手三步：
// /api/chat/create → /api/chat/agent-stream → /api/agent/stream/connect
func newFakeUpstream(t *testing.T, gotHeaders *map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("access-token") != "tk" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"code":401,"msg":"未登录"}`))
			return
		}
		var body createChatBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !strings.HasPrefix(body.ChatID, "desk-") || len(body.ChatID) != len("desk-")+16 {
			t.Errorf("chatId 格式不对: %q", body.ChatID)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"chatId":"x"}}`))
	})
	mux.HandleFunc("/api/chat/agent-stream", func(w http.ResponseWriter, r *http.Request) {
		var body agentStreamBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) != 1 || body.Messages[0].Content[0].Text == "" {
			t.Errorf("messages 拼装异常: %+v", body.Messages)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"conversationId":"conv-1","streamMessageId":"1","success":true}}`))
	})
	connectCall := 0
	mux.HandleFunc("/api/agent/stream/connect", func(w http.ResponseWriter, r *http.Request) {
		connectCall++
		if gotHeaders != nil {
			(*gotHeaders)["passport"] = r.Header.Get("X-Passport-Token")
			(*gotHeaders)["cookie"] = r.Header.Get("Cookie")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["conversationId"] != "conv-1" {
			t.Errorf("conversationId 未透传: %v", body["conversationId"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		var frames []string
		if connectCall == 1 {
			// 第一次连接：回放用户消息 → 两个增量 → 无终态帧直接断开（模拟断流）
			frames = []string{
				`{"headlessRemoteAgentResp":{"isHistoryMessage":true,"historyMessages":[` +
					`{"type":"user","content":[{"type":"text","text":"你好"}]}]}}`,
				`{"headlessRemoteAgentResp":{"currentStatus":"running"}}`,
				`{"headlessRemoteAgentResp":{"isHistoryMessage":false,"message":{"type":"assistant","messageId":"m1","content":[{"type":"text","text":"Hello"}],"finished":false}}}`,
				`{"headlessRemoteAgentResp":{"isHistoryMessage":false,"message":{"type":"assistant","messageId":"m1","content":[{"type":"text","text":", world"}],"finished":false}}}`,
			}
		} else {
			// 第二次连接（重连）：历史里已有 assistant 全文 → 只补差值 → 终态
			frames = []string{
				`{"headlessRemoteAgentResp":{"isHistoryMessage":true,"historyMessages":[` +
					`{"type":"user","content":[{"type":"text","text":"你好"}]},` +
					`{"type":"assistant","messageId":"m1","modelDisplayName":"LongCat-2.0-Agent","content":[{"type":"text","text":"Hello, world"}]}]}}`,
				`{"headlessRemoteAgentResp":{"currentStatus":"completed"}}`,
			}
		}
		for _, f := range frames {
			fmt.Fprintf(w, "data:%s\n\n", f)
			fl.Flush()
		}
	})
	return httptest.NewServer(mux)
}

func TestChatEndToEnd(t *testing.T) {
	var hdr = map[string]string{}
	srv := newFakeUpstream(t, &hdr)
	defer srv.Close()

	cfg := config.Default()
	cfg.Upstream.ChatBase = srv.URL
	cfg.Upstream.StreamBase = srv.URL
	cfg.Upstream.GatewayBase = srv.URL
	cfg.Upstream.ConnectRetries = 3

	c := New(cfg)
	acc := &account.Account{ID: "a1", Token: "tk", Enabled: true}

	var deltas []string
	res, err := c.Chat(context.Background(), acc, "你好", "LongCat-2.0", func(s string) error {
		deltas = append(deltas, s)
		return nil
	})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	// 首连发 Hello + ", world"；重连历史已有 "Hello, world"，不应重复输出
	if got := strings.Join(deltas, ""); got != "Hello, world" {
		t.Errorf("增量去重错误，deltas=%q", got)
	}
	if res.Text != "Hello, world" {
		t.Errorf("全文错误: %q", res.Text)
	}
	if res.ConversationID != "conv-1" {
		t.Errorf("conversationId 错误: %q", res.ConversationID)
	}
	if res.Model != "LongCat-2.0-Agent" {
		t.Errorf("模型回填错误: %q", res.Model)
	}
	if res.Status != "completed" {
		t.Errorf("状态错误: %q", res.Status)
	}
	if hdr["passport"] != "tk" {
		t.Errorf("事件流未带 X-Passport-Token: %q", hdr["passport"])
	}
	if hdr["cookie"] != "X-Passport-Token=tk" {
		t.Errorf("事件流 Cookie 错误: %q", hdr["cookie"])
	}
}

func TestCheckToken401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"code":401,"msg":"未登录"}`))
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.Upstream.GatewayBase = srv.URL
	_, _, err := New(cfg).CheckToken(context.Background(), "bad")
	if err == nil {
		t.Fatal("期望鉴权错误")
	}
	var ue *Error
	if !asErr(err, &ue) || ue.Kind != KindAuth {
		t.Fatalf("期望 KindAuth，得到 %v", err)
	}
}

func asErr(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
