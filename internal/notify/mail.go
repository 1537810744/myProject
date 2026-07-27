// Package notify 邮件通知工具（预警模块、自动交易模块使用）。
// 使用标准库 net/smtp，SSL 直连（465 端口），个人工具够用。
package notify

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"deltacrypto/internal/service/settings"
)

// SendMail 发送一封 UTF-8 文本邮件。配置不完整时返回错误，调用方自行降级（仅记录日志）。
func SendMail(cfg settings.MailConfig, subject, body string) error {
	if !cfg.Enabled() {
		return fmt.Errorf("邮件配置不完整（请在设置页填写 SMTP 与收件邮箱）")
	}
	from := cfg.From
	if from == "" {
		from = cfg.User
	}

	// 组装邮件（RFC 822）；中文主题按 RFC 2047 做 Base64 编码
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + cfg.To + "\r\n")
	b.WriteString("Subject: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(subject)) + "?=\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n" + body + "\r\n")

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	auth := smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)

	// SSL 直连（465 端口，QQ/163/Gmail 均支持）
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, &tls.Config{ServerName: cfg.Host})
	if err != nil {
		return fmt.Errorf("SMTP 连接失败: %w", err)
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP 客户端创建失败: %w", err)
	}
	defer client.Close()

	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("SMTP 认证失败: %w", err)
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	if err := client.Rcpt(cfg.To); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(b.String())); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}
