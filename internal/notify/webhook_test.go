package notify

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"deltacrypto/internal/service/settings"
)

// TestSendWebhook 正常发送：假服务器收到 POST，正文含 subject 和 body。
func TestSendWebhook(t *testing.T) {
	received := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := SendWebhook(srv.URL, "测试标题", "通知正文"); err != nil {
		t.Fatalf("SendWebhook 失败: %v", err)
	}
	msg := <-received
	if !strings.HasPrefix(msg, "{") { // 应以 { 开头 = 是 JSON 对象
		t.Fatalf("应发 JSON: %s", msg)
	}
	if !strings.Contains(msg, "测试标题") || !strings.Contains(msg, "通知正文") {
		t.Fatalf("webhook 正文不对: %s", msg)
	}
}

// TestSendWebhookNon2xx 非 2xx 响应应报错。
func TestSendWebhookNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // 500
	}))
	defer srv.Close()

	if err := SendWebhook(srv.URL, "s", "b"); err == nil {
		t.Fatal("非 2xx 响应应返回错误")
	}
}

// TestSendWebhookEmptyURL 空 URL 直接报错（不 panic）。
func TestSendWebhookEmptyURL(t *testing.T) {
	if err := SendWebhook("", "s", "b"); err == nil {
		t.Fatal("空 URL 应报错")
	}
}

// TestNotifyNoChannel 没配任何通道时，Notify 静默成功（不报错不发送）。
func TestNotifyNoChannel(t *testing.T) {
	// 用户没配置邮件也没配置 webhook：不应报错（静默跳过），也不应 panic。
	if err := Notify(settings.MailConfig{}, "s", "b"); err != nil {
		t.Fatalf("无任何通知通道时应静默成功，实际报错: %v", err)
	}
}
