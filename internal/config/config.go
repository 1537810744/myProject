// Package config 负责读取程序运行所需的环境配置。
// 设计原则：个人本机小工具，配置项极少，全部通过环境变量覆盖，缺省值即可直接跑起来。
package config

import (
	"os"
	"strconv"
)

// Config 程序全局配置
type Config struct {
	// ListenAddr HTTP 服务监听地址。
	// 默认 127.0.0.1:8080（本机使用，无需鉴权）；
	// 在 Docker 中需要改成 0.0.0.0:8080 才能从宿主机访问。
	ListenAddr string
	// DBPath SQLite 数据库文件路径
	DBPath string
}

// Load 从环境变量加载配置，未设置时使用默认值
func Load() *Config {
	return &Config{
		ListenAddr: getEnv("LISTEN_ADDR", "127.0.0.1:8080"),
		DBPath:     getEnv("DB_PATH", "./data/deltacrypto.db"),
	}
}

// getEnv 读取字符串环境变量，缺省时返回 fallback
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getEnvInt 读取整型环境变量（保留工具函数，方便后续扩展）
func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
