package viewerapp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
)

func (c *client) writeInput(w http.ResponseWriter, r *http.Request, method string, params any) {
	data, err := json.Marshal(params)
	if err != nil {
		http.Error(w, "invalid input", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := c.conn.WriteMessageContext(ctx, protocol.Message{Kind: "event", Method: method, Params: data}); err != nil {
		http.Error(w, "输入发送失败或通道繁忙，请重试", 502)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
