// 【通知】Webhook 通道：POST 一个 JSON 到配置的 URL（Server酱/企业微信机器人/自建服务等）。
// 作为邮件的第二通道：邮件可能进垃圾箱/超时，webhook 通常更即时可靠。
// 语法点：net/http 客户端、json.Marshal、timeout、io.ReadAll、状态码判断。
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"deltacrypto/internal/service/settings"
)

// webhookTimeout 发 webhook 的超时（别把主流程卡死）。
const webhookTimeout = 10 * time.Second

// webhookPayload 发出去的 JSON 结构（不同机器人要求的字段名不一，这里给常见两种：
// subject + text，兼容大多数）。扩展字段可后续按需加。
type webhookPayload struct {
	Subject string `json:"subject"`
	Text    string `json:"text"`
}

// SendWebhook 向 url POST 一条 JSON 通知。url 为空或请求失败返回错误。
func SendWebhook(url, subject, body string) error {
	// json.Marshal：把结构体转成 JSON 字节。err 基本不会发生（结构体简单）。
	payload, err := json.Marshal(webhookPayload{Subject: subject, Text: body})
	if err != nil {
		return err
	}
	// 构造 HTTP POST 请求：bytes.NewReader 把字节切片包装成 io.Reader（请求体）。
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("构造 webhook 请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// http.Client 带超时：防止对方服务器不响应把通知流程卡住。
	client := &http.Client{Timeout: webhookTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook 请求失败: %w", err)
	}
	// 响应体用完必须关，否则连接泄漏（defer 保证）。
	defer resp.Body.Close()
	// 非 2xx 状态码都算失败；顺手读响应体便于排查。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512)) // 最多读 512 字节，防超大响应
		return fmt.Errorf("webhook 返回非 2xx: %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// Notify 发通知（邮件 + webhook 双通道）：配置了哪个就发哪个，都成功才返回 nil。
// 调用方（预警/自动交易）用它替代直接 SendMail——多通道冗余，一个坏了另一个顶上。
func Notify(cfg settings.MailConfig, subject, body string) error {
	var errs []string
	if cfg.Enabled() { // 邮件配好了就发邮件
		if err := SendMail(cfg, subject, body); err != nil {
			errs = append(errs, "邮件: "+err.Error())
		}
	}
	if cfg.WebhookURL != "" { // webhook 配好了就发 webhook
		if err := SendWebhook(cfg.WebhookURL, subject, body); err != nil {
			errs = append(errs, "webhook: "+err.Error())
		}
	}
	if len(errs) == 0 {
		return nil // 全部成功（或没有通道可发但不报错——由调用方决定要不要关心）
	}
	// 有失败：把失败原因汇总返回，调用方决定是否降级为只记日志。
	return fmt.Errorf("通知部分失败: %s", strings.Join(errs, "；"))
}
