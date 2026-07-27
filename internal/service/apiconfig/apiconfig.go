// Package apiconfig 模块 1：API 配置模块。
//
// 需求要点：
//   - 前端传入交易所 API key/secret，保存到数据库；
//   - 测试交易所的 API 连通性与权限可行性，返回验证结果给前端展示。
package apiconfig

import (
	"fmt"
	"time"

	"deltacrypto/internal/database"
	"deltacrypto/internal/exchange"
	"deltacrypto/internal/model"
)

// Service API 配置模块服务
type Service struct {
	db  *database.DB
	hub *exchange.Hub // 保存凭证后通知 Hub 热更新连接
}

// New 创建 API 配置模块
func New(db *database.DB, hub *exchange.Hub) *Service {
	return &Service{db: db, hub: hub}
}

// Save 保存（覆盖式）某交易所的 API 凭证，并热更新交易所连接。
// 同一交易所只保留最新一条（个人工具单账户场景）。
func (s *Service) Save(exchangeID, label, apiKey, apiSecret string) error {
	if apiKey == "" || apiSecret == "" {
		return fmt.Errorf("API Key 与 Secret 不能为空")
	}
	// 简单起见：先删后插，保证每个交易所只有一条有效凭证
	if _, err := s.db.Exec(`DELETE FROM exchange_api WHERE exchange = ?`, exchangeID); err != nil {
		return err
	}
	if _, err := s.db.Exec(
		`INSERT INTO exchange_api(exchange, label, api_key, api_secret) VALUES(?, ?, ?, ?)`,
		exchangeID, label, apiKey, apiSecret); err != nil {
		return err
	}
	// 凭证变更后热重建连接；失败不阻断保存（可能另一腿交易所还没配）
	_ = s.hub.Reload()
	return nil
}

// List 列出已保存的凭证（secret 打码返回，前端只展示不泄露）
func (s *Service) List() ([]model.ExchangeAPI, error) {
	rows, err := s.db.Query(`SELECT id, exchange, label, api_key, api_secret, created_at FROM exchange_api ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.ExchangeAPI, 0) // 初始化为空数组，保证 JSON 返回 [] 而非 null
	for rows.Next() {
		var a model.ExchangeAPI
		if err := rows.Scan(&a.ID, &a.Exchange, &a.Label, &a.APIKey, &a.APISecret, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.APISecret = maskSecret(a.APISecret) // 密钥脱敏
		out = append(out, a)
	}
	return out, rows.Err()
}

// Delete 删除某交易所凭证
func (s *Service) Delete(exchangeID string) error {
	_, err := s.db.Exec(`DELETE FROM exchange_api WHERE exchange = ?`, exchangeID)
	return err
}

// Test 测试指定凭证的连通性与权限（不入库，直接临时建连接测试）。
// 若 apiKey 为空，则使用数据库中已保存的凭证测试。
func (s *Service) Test(exchangeID, role, apiKey, apiSecret string) model.APITestResult {
	result := model.APITestResult{Exchange: exchangeID}

	// 未传凭证则读库中已保存的
	if apiKey == "" {
		row := s.db.QueryRow(
			`SELECT api_key, api_secret FROM exchange_api WHERE exchange = ? ORDER BY id DESC LIMIT 1`, exchangeID)
		if err := row.Scan(&apiKey, &apiSecret); err != nil {
			result.Message = "未找到已保存的凭证，请先填写 API Key"
			return result
		}
	}

	// 临时创建连接（LoadMarkets 顺便验证了公共接口可达性）
	ex, err := exchange.New(exchangeID, role, apiKey, apiSecret)
	if err != nil {
		result.Message = fmt.Sprintf("连接失败: %v", err)
		return result
	}

	// 1. 公共连通性
	if err := ex.TestPublic(); err != nil {
		result.Message = fmt.Sprintf("公共接口连通失败: %v", err)
		return result
	}
	result.Connected = true

	// 2. 私有权限（读余额即可证明 key 有效且具备读取权限）
	if err := ex.TestPrivate(); err != nil {
		result.Message = fmt.Sprintf("连通成功，但私有权限验证失败（检查 Key 权限/IP 白名单）: %v", err)
		return result
	}
	result.Permission = true
	result.Message = fmt.Sprintf("测试连接成功，权限正常（%s，测试时间 %s）",
		exchangeID, time.Now().Format("15:04:05"))
	return result
}

// maskSecret 密钥脱敏：只保留前 3 位，其余打星
func maskSecret(s string) string {
	if len(s) <= 3 {
		return "***"
	}
	return s[:3] + "******"
}
