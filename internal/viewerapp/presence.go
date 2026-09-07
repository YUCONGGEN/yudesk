package viewerapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

func checkViewerPresence(ctx context.Context, server, id string) error {
	endpoint, err := viewerStatusEndpoint(server, []string{id})
	if err != nil {
		return errors.New("状态服务器配置无效")
	}
	client, transport := viewerHTTPClient(5 * time.Second)
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("状态请求无效")
	}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("无法检测设备在线状态，请稍后重试")
	}
	defer response.Body.Close()
	var result struct {
		Devices []struct {
			ID        string `json:"id"`
			Online    bool   `json:"online"`
			Active    bool   `json:"active"`
			Connected bool   `json:"connected"`
			Ready     *bool  `json:"ready"`
		} `json:"devices"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&result) != nil {
		return errors.New("设备状态检测失败，请稍后重试")
	}
	for _, status := range result.Devices {
		if status.ID != id {
			continue
		}
		if !status.Online {
			return errors.New("被控端不在线，请先打开被控端")
		}
		if status.Connected {
			return errors.New("设备已有连接，请结束原连接后重试")
		}
		if !status.Active {
			return errors.New("设备尚未激活或授权已到期")
		}
		if status.Ready != nil && !*status.Ready {
			return errors.New("被控端正在准备连接通道，请稍后重试")
		}
		return nil
	}
	return errors.New("未能取得该设备的在线状态")
}
