// 【阅读顺序 15】邮件通知工具。用标准库 net/smtp（SSL 直连 465）发一封 UTF-8 文本邮件。
// 为什么自己拼 SMTP 而不是用第三方库？—— 发邮件就是按协议跟服务器对话，标准库
// net/smtp 已覆盖全部流程，整个函数不到 60 行，引入库反而多一层依赖。标准库够用就不加。
// 语法点预览：if 早失败、strings.Builder、base64 编码、tls.DialWithDialer、&结构体{}、
// defer、error 包装、多返回值。
package notify

// import 导入用到的包。
import (
	"crypto/tls"      // TLS 加密连接（SSL 直连用）
	"encoding/base64" // base64 编码（中文主题要编码）
	"fmt"             // 格式化
	"net"             // 网络拨号：net.Dialer
	"net/smtp"        // SMTP 协议客户端（标准库）
	"strings"         // strings.Builder（高效拼接字符串）
	"time"            // 超时时间

	"deltacrypto/internal/service/settings" // 邮件配置（MailConfig 定义在这）
)

// SendMail 发送一封 UTF-8 文本邮件。配置不完整时返回错误，调用方自行降级为只写日志。
// 函数签名：入参 (cfg settings.MailConfig, subject, body string)，返回 error。
func SendMail(cfg settings.MailConfig, subject, body string) error {
	// 前置校验放函数最前——“早失败”模式：配置不全就不可能有结果，先挡在门口。
	// cfg.Enabled() 是 MailConfig 的方法（见 settings.go），判断 4 个关键字段是否齐全。
	if !cfg.Enabled() { // “!” 是取反：Enabled() 返回 false 时进入
		return fmt.Errorf("邮件配置不完整（请在设置页填写 SMTP 与收件邮箱）")
	}
	from := cfg.From // 先默认用 From 字段
	if from == "" {  // 如果没填 From
		from = cfg.User // 用登录用户名兜底
	}

	// —— 组装邮件正文（RFC 822 格式：每行头 + 空行 + 正文）——
	var b strings.Builder // 声明 strings.Builder（高效的字符串拼接器）
	// 为什么用 Builder 而不是 s += ...？—— 字符串不可变，+= 每拼一次就新建一个字符串
	// 对象；Builder 内部用字节数组累积、最后一次性生成，更快。
	b.WriteString("From: " + from + "\r\n") // WriteString 往 Builder 里追加一段字符串
	b.WriteString("To: " + cfg.To + "\r\n")
	// 中文主题不能直接写进 Subject:，会乱码。按 RFC 2047 规范把中文 base64 编码后
	// 包一层 =?UTF-8?B?...?=，收件端会自动解码还原。
	// base64.StdEncoding.EncodeToString([]byte(subject))：[]byte(...) 把字符串转字节数组，
	// 再 base64 编码成字符串。
	b.WriteString("Subject: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(subject)) + "?=\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n" + body + "\r\n") // 邮件头结束后必须空一行（\r\n），才是正文

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port) // 服务器地址：主机:端口
	// smtp.PlainAuth：用户名 + 密码/授权码登录。QQ/163 邮箱的“SMTP 授权码”就在这用。
	auth := smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)

	// —— SSL 直连 465 端口 ——
	// 为什么不用 smtp.SendMail 那个简单函数？—— SendMail 只支持“先明文再升级 TLS”
	// 的 587 流程，而中文邮箱普遍用 465（一开始就加密）。所以手动 tls.Dial 一条
	// 加密连接，再包成 smtp.Client。这就是“理解协议细节才能用对标准库”的例子。
	// &net.Dialer{Timeout: 15 * time.Second}：带超时的拨号器（15 秒连不上就放弃）。
	// time.Second * 15 = 15 秒（time.Duration 是纳秒整数，可乘除）。
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, &tls.Config{ServerName: cfg.Host})
	if err != nil {
		return fmt.Errorf("SMTP 连接失败: %w", err) // %w 包装错误，保留底层原因
	}
	// smtp.NewClient：把连接包装成 SMTP 客户端（负责按协议对话）。
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP 客户端创建失败: %w", err)
	}
	// defer 注册“函数返回前一定执行”的动作：关闭连接。
	// 无论中间哪一步出错 return，连接都被关闭，不泄漏。这是 Go 管理资源的惯用法。
	defer client.Close()

	// —— 下面就是跟 SMTP 服务器“逐句对话”（协议顺序不能乱）——
	if err := client.Auth(auth); err != nil { // 第 1 句：登录认证
		return fmt.Errorf("SMTP 认证失败: %w", err)
	}
	if err := client.Mail(from); err != nil { // 第 2 句：声明发件人
		return err
	}
	if err := client.Rcpt(cfg.To); err != nil { // 第 3 句：声明收件人
		return err
	}
	w, err := client.Data() // 第 4 句：进入“写正文”阶段，返回一个写入器 w
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(b.String())); err != nil { // 把拼好的邮件正文写进去
		return err
	}
	if err := w.Close(); err != nil { // 结束正文
		return err
	}
	return client.Quit() // 第 5 句：说 QUIT，礼貌结束会话
}
