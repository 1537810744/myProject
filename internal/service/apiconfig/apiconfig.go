// 【阅读顺序 07】模块 1：API 配置。
// 职责：保存交易所 Key/Secret 到数据库（每所留最新一条，列表接口脱敏），
// 以及“测试连接”——临时建连验证公共连通性 + 私有权限。
// 关键设计：保存后调用 hub.Reload() 热更新连接，填好 Key 不用重启就能交易。
// 语法点预览：struct、指针接收者、QueryRow/Query/rows.Next、空标识符 _、if 带初始化、
// defer、错误处理、字符串切片。
package apiconfig

// import 导入用到的包。
import (
	"fmt"  // 格式化
	"log"  // 日志（加密失败的警告）
	"time" // 时间

	"deltacrypto/internal/crypto"   // 凭证加密
	"deltacrypto/internal/database" // 数据库
	"deltacrypto/internal/exchange" // 交易所抽象层
	"deltacrypto/internal/model"    // 数据结构
)

// Service API 配置模块服务。
// 为什么持有 hub？—— Save 后要立即重建交易连接。如果只存库不持 hub，连接还是旧的，
// 用户得重启，体验差。持一个依赖并主动协调，是模块协作的常规做法。
// 为什么持有 keyring？—— Save 时要把 API Secret 加密后落库（安全升级）。
type Service struct {
	db      *database.DB    // 数据库
	hub     *exchange.Hub   // 交易所连接管理器
	keyring *crypto.Keyring // 密钥环（加密 Secret 用）
}

// New 创建 API 配置模块。
func New(db *database.DB, hub *exchange.Hub, keyring *crypto.Keyring) *Service {
	return &Service{db: db, hub: hub, keyring: keyring} // 复合字面量创建对象
}

// Save 保存（覆盖式）某交易所的 API 凭证，并热更新交易所连接。
// 同一交易所只保留最新一条：个人工具单账户场景，多账户反而让人困惑该用哪条。
func (s *Service) Save(exchangeID, label, apiKey, apiSecret string) error {
	// 前置校验（“早失败”模式）：Key/Secret 不能为空。
	if apiKey == "" || apiSecret == "" { // || 或
		return fmt.Errorf("API Key 与 Secret 不能为空")
	}
	// “先删后插”保证每个交易所只有一条有效凭证。
	// 为什么不用 UPDATE？—— 语义更直白（要的就是“只剩最新一条”），还顺带清掉历史脏数据。
	// 代价：两步不是原子操作（中途崩会丢旧凭证），个人工具可接受。
	if _, err := s.db.Exec(`DELETE FROM exchange_api WHERE exchange = ?`, exchangeID); err != nil {
		return err
	}
	// 安全：Secret 加密后落库。加密失败就回退明文并记日志（宁可存明文也不让保存失败，
	// 但启动时会有明显警告提醒配置 MASTER_KEY）。这是"尽力加密"的取舍。
	storedSecret := apiSecret
	if s.keyring != nil {
		if enc, err := s.keyring.Encrypt(apiSecret); err == nil {
			storedSecret = enc
		} else {
			log.Printf("[安全警告] Secret 加密失败，将以明文存储（建议配置 MASTER_KEY）: %v", err)
		}
	}
	if _, err := s.db.Exec(
		`INSERT INTO exchange_api(exchange, label, api_key, api_secret) VALUES(?, ?, ?, ?)`,
		exchangeID, label, apiKey, storedSecret); err != nil {
		return err
	}
	// 凭证变更后热重建连接。失败不阻断保存——主流程（存凭证）已经成功，
	// Reload 只是顺带的热更新（可能另一腿交易所还没配好），失败不该让保存跟着失败。
	// “_ =” 是显式忽略返回值：Go 规定声明了不用=编译错误，忽略必须【故意】表态。
	_ = s.hub.Reload()
	return nil
}

// List 列出已保存的凭证（secret 脱敏后返回）。
func (s *Service) List() ([]model.ExchangeAPI, error) {
	rows, err := s.db.Query(`SELECT id, exchange, label, api_key, api_secret, created_at FROM exchange_api ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()                  // ⚠️ rows 用完必须关，否则数据库连接一直被占着（defer 保证关闭）
	out := make([]model.ExchangeAPI, 0) // 空数组而非 nil：JSON 返回 [] 而非 null
	for rows.Next() {                   // rows 是“游标”：Next() 判断有没有下一行
		var a model.ExchangeAPI // 每行创建一个结构体接收
		// Scan：把当前行的各列按顺序填进 a 的各字段（顺序必须和 SELECT 的列一致）。
		if err := rows.Scan(&a.ID, &a.Exchange, &a.Label, &a.APIKey, &a.APISecret, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.APISecret = maskSecret(a.APISecret) // 密钥脱敏（只留前 3 位）
		out = append(out, a)                  // 加入结果切片
	}
	// 循环结束后还要检查一次 rows.Err()：遍历中途可能出错（连接断开等），
	// 只在循环里检查 Scan 错误是不够的。
	return out, rows.Err()
}

// Delete 删除某交易所凭证。
func (s *Service) Delete(exchangeID string) error {
	_, err := s.db.Exec(`DELETE FROM exchange_api WHERE exchange = ?`, exchangeID)
	return err
}

// Test 测试指定凭证的连通性与权限（不入库，直接临时建连接测试）。
// apiKey 传空则用数据库里已保存的凭证测。
func (s *Service) Test(exchangeID, role, apiKey, apiSecret string) model.APITestResult {
	result := model.APITestResult{Exchange: exchangeID} // 先初始化结果对象

	// 未传凭证 → 读库中已保存的。
	if apiKey == "" {
		// QueryRow 查单行，Scan 填进 apiKey/apiSecret。
		row := s.db.QueryRow(
			`SELECT api_key, api_secret FROM exchange_api WHERE exchange = ? ORDER BY id DESC LIMIT 1`, exchangeID)
		if err := row.Scan(&apiKey, &apiSecret); err != nil {
			result.Message = "未找到已保存的凭证，请先填写 API Key"
			return result
		}
	}

	// 临时建一个【独立】连接来测——为什么不用 hub 里现成的？
	//   用户可能还没保存新 Key，我们要测的是【新填的 Key 是否可用】，
	//   必须用它临时建连，不能动正在用的连接。
	// 顺便：exchange.New 内部会 LoadMarkets，这一步本身就验证了公共接口可达。
	ex, err := exchange.New(exchangeID, role, apiKey, apiSecret)
	if err != nil {
		result.Message = fmt.Sprintf("连接失败: %v", err)
		return result
	}

	// 第 1 关：公共连通性（拉服务器时间）。ex.TestPublic() 返回 error。
	if err := ex.TestPublic(); err != nil {
		result.Message = fmt.Sprintf("公共接口连通失败: %v", err)
		return result
	}
	result.Connected = true // 过关：标记连通

	// 第 2 关：私有权限（读余额——能读到就证明 Key 有效且有读取权限）。
	if err := ex.TestPrivate(); err != nil {
		result.Message = fmt.Sprintf("连通成功，但私有权限验证失败（检查 Key 权限/IP 白名单）: %v", err)
		return result
	}
	result.Permission = true // 过关：标记权限正常
	result.Message = fmt.Sprintf("测试连接成功，权限正常（%s，测试时间 %s）",
		exchangeID, time.Now().Format("15:04:05")) // time.Now() 当前时间；Format 按模板输出时间字符串
	// ⚠️ "15:04:05" 不是随手写的——Go 用【固定参考时间】当天数模板：
	// 2006-01-02 15:04:05 代表“年-月-日 时:分:秒”。数字摆哪个位置，就输出哪部分。
	// 这里只写了 15:04:05，所以只输出“时:分:秒”。这是 Go 独有的、最反直觉的坑之一。
	return result
}

// maskSecret 密钥脱敏：只保留前 3 位，其余打星。
// 保留前 3 位：让用户能凭前缀认出是哪把 Key，同时无法从网页抄走完整密钥。
func maskSecret(s string) string {
	if len(s) <= 3 { // len() 字符串长度（按字节）
		return "***"
	}
	return s[:3] + "******" // 切片取前 3 个字符 + 固定星号
}
