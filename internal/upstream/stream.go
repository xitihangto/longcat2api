package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/linquin/longcat2api/internal/account"
)

// errStopRead 表示已收到终态帧，主动结束读取（上游不会主动关连接）。
var errStopRead = errors.New("terminal frame received")

// agentMsg 是上游消息体（user / assistant）。
type agentMsg struct {
	ConversationID string        `json:"conversationId"`
	Type           string        `json:"type"`
	MessageID      string        `json:"messageId"`
	Model          string        `json:"model"`
	ModelDisplay   string        `json:"modelDisplayName"`
	Finished       bool          `json:"finished"`
	Status         int           `json:"status"`
	Content        []contentPart `json:"content"`
}

func (m *agentMsg) text() string {
	var b strings.Builder
	for _, p := range m.Content {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// sseFrame 是 /api/agent/stream/connect 的 data 行结构。
type sseFrame struct {
	Headless *struct {
		IsHistoryMessage bool        `json:"isHistoryMessage"`
		HistoryMessages  []*agentMsg `json:"historyMessages"`
		Message          *agentMsg   `json:"message"`
		CurrentStatus    string      `json:"currentStatus"`
	} `json:"headlessRemoteAgentResp"`
	StatusUpdate string `json:"statusUpdate"`
	// 部分帧是错误直出
	Code *int   `json:"code"`
	Msg  string `json:"msg"`
}

// connectStream 订阅事件流并把增量文本回调出去。
func (c *Client) connectStream(ctx context.Context, acc *account.Account, convID string, res *Result, onDelta func(string) error) error {
	attempts := c.cfg.Upstream.ConnectRetries
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	var acc0 string // 已输出文本，用于重连去重
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		terminal, text, err := c.streamOnce(ctx, acc, convID, res, onDelta, &acc0)
		res.Text = text
		if terminal {
			return nil
		}
		if err != nil {
			lastErr = err
			var ue *Error
			if errors.As(err, &ue) {
				switch ue.Kind {
				case KindAuth, KindQuota, KindBadRequest, KindRateLimit:
					return err // 重连也没用
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return errOf(KindUpstream, 502, 0, "事件流未收到终态帧（共重连 %d 次）", attempts)
}

// streamOnce 建一次连接并消费到结束；返回是否拿到终态帧。
func (c *Client) streamOnce(ctx context.Context, acc *account.Account, convID string, res *Result, onDelta func(string) error, acc0 *string) (bool, string, error) {
	body, _ := json.Marshal(map[string]any{
		"timestamp":       time.Now().UnixMilli(),
		"conversationId":  convID,
		"messageIndex":    0,
		"incrementalOnly": true,
	})
	url := c.cfg.Upstream.StreamBase + "/api/agent/stream/connect"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, *acc0, err
	}
	c.decorateUserAgent(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("X-Passport-Token", acc.Token)
	req.Header.Set("Cookie", "X-Passport-Token="+acc.Token)

	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, *acc0, ctx.Err()
		}
		return false, *acc0, errOf(KindNetwork, 0, 0, "%s", err.Error())
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !strings.Contains(ct, "text/event-stream") {
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		return false, *acc0, classifyHTTP(resp.StatusCode, buf[:n], url)
	}

	terminal := false
	var lastMsgID string
	text := *acc0

	handle := func(line string) error {
		if !strings.HasPrefix(line, "data:") {
			return nil
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			return nil
		}
		var f sseFrame
		if err := json.Unmarshal([]byte(payload), &f); err != nil {
			return nil // 非 JSON 帧忽略
		}
		if f.StatusUpdate != "" {
			switch f.StatusUpdate {
			case "completed":
				terminal = true
				res.Status = "completed"
			case "canceled":
				terminal = true
				res.Status = "canceled"
			case "error", "failed":
				terminal = true
				res.Status = "error"
			}
			if terminal {
				return errStopRead
			}
			return nil
		}
		if f.Headless == nil {
			return nil
		}
		h := f.Headless
		// 历史回放：仅用于重连时补齐差值
		if h.IsHistoryMessage {
			for _, m := range h.HistoryMessages {
				if m == nil || m.Type != "assistant" {
					continue
				}
				full := m.text()
				if m.ModelDisplay != "" {
					res.Model = m.ModelDisplay
				} else if m.Model != "" {
					res.Model = m.Model
				}
				if strings.HasPrefix(full, text) && len(full) > len(text) {
					delta := full[len(text):]
					text = full
					if onDelta != nil {
						if err := onDelta(delta); err != nil {
							return err
						}
					}
				}
			}
			return nil
		}
		if h.CurrentStatus != "" {
			res.Status = h.CurrentStatus
			if h.CurrentStatus == "completed" || h.CurrentStatus == "error" || h.CurrentStatus == "canceled" {
				terminal = true
				return errStopRead
			}
			return nil
		}
		if m := h.Message; m != nil {
			if m.Type != "assistant" {
				return nil
			}
			if m.ModelDisplay != "" {
				res.Model = m.ModelDisplay
			} else if m.Model != "" {
				res.Model = m.Model
			}
			if m.MessageID != "" && m.MessageID != lastMsgID {
				// 新的一条 assistant 消息（例如多轮内部执行），续写
				lastMsgID = m.MessageID
			}
			t := m.text()
			if t != "" {
				text += t
				if onDelta != nil {
					if err := onDelta(t); err != nil {
						return err
					}
				}
			}
			if m.Finished {
				res.Status = "completed"
			}
			return nil
		}
		return nil
	}

	if err := readLines(resp.Body, handle); err != nil {
		if errors.Is(err, errStopRead) {
			return true, text, nil
		}
		if ctx.Err() != nil {
			return terminal, text, ctx.Err()
		}
		if !terminal {
			return false, text, errOf(KindNetwork, 0, 0, "事件流中断: %s", err)
		}
	}
	if !terminal && res.Status == "" {
		res.Status = "error"
	}
	return terminal, text, nil
}

// CheckToken 校验 token 是否有效，返回用户信息。用于账号导入/巡检。
func (c *Client) CheckToken(ctx context.Context, token string) (uid, name string, err error) {
	url := c.cfg.Upstream.GatewayBase + "/api/gateway/passport/current-user"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	c.decorateUserAgent(req)
	req.Header.Set("X-Auth-Token", token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", "", errOf(KindNetwork, 0, 0, "%s", err.Error())
	}
	defer resp.Body.Close()
	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	raw := buf[:n]
	if resp.StatusCode != 200 {
		return "", "", classifyHTTP(resp.StatusCode, raw, url)
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			UserID   int64  `json:"userId"`
			UserName string `json:"userName"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", "", fmt.Errorf("响应解析失败: %s", snippet(raw))
	}
	if env.Code != 0 {
		return "", "", classifyCode(env.Code, string(raw), resp.StatusCode)
	}
	return fmt.Sprintf("%d", env.Data.UserID), env.Data.UserName, nil
}
