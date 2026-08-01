// 【阅读顺序 02】环境配置。
// 本文件：读程序所需的环境变量（监听地址/DB路径/代理），给默认值。
// 项目配置极少，用环境变量（Docker 部署标准姿势）而非配置文件；缺省值保证零配置能跑。
// 语法点预览：struct、字段、&Config{...} 复合字面量、:=、if 带初始化、可变参数、多返回值。
package config

// import 导入标准库（路径没有项目前缀 = 标准库或第三方库）。
import (
	"os" // 读取环境变量：os.Getenv
)

// Config 程序全局配置。
// 为什么用【结构体 struct】？—— 把相关的配置字段装进一个“模具”，
// 调用方拿一个 Config 对象就拿到全部配置，比散落的一堆变量好管理。
type Config struct {
	// ListenAddr HTTP 监听地址（string 类型，默认值是空串，但 Load 里会填默认）。
	// 默认 127.0.0.1:8080：本机使用、无需鉴权；容器里要改成 0.0.0.0:8080 才能被宿主机访问。
	ListenAddr string
	// DBPath SQLite 数据库文件路径。
	DBPath string
	// ProxyURL HTTP 代理。大陆直连交易所会被墙，本机运行常要配。留空=直连。
	ProxyURL string
	// AuthToken API 鉴权令牌。留空=不鉴权（本机 localhost 默认）；设置了之后，
	// 所有 /api/* 请求必须带 `Authorization: Bearer <AuthToken>` 头，否则返回 401。
	// 用途：把服务暴露到局域网/云服务器时加一道最基本的访问控制。
	// ⚠️ 注意：启用后前端页面需要配置带上这个头（或用 curl 等工具直连 API）。
	AuthToken string
}

// Load 从环境变量加载配置（构造函数——Go 里“负责创建对象”的函数）。
// 为什么返回 *Config（指针）而不是 Config（值）？
//
//	① Go 惯例：构造函数返回指针，避免大结构体整块拷贝；
//	② 函数返回的局部变量地址由 Go 自动“逃逸”到堆上，调用方能安全使用，无需手动管理内存。
func Load() *Config {
	// “&Config{...}” 是【复合字面量】：分配一个 Config 并初始化字段。
	// 前面的 & 取它的地址 → 结果类型是 *Config（指针）。
	// 字段带名字写（ListenAddr:），可读性好，且结构体字段顺序变了也不影响。
	return &Config{
		// getEnv(key, fallback)：读环境变量，没设置就返回 fallback（默认值）。
		ListenAddr: getEnv("LISTEN_ADDR", "127.0.0.1:8080"),
		DBPath:     getEnv("DB_PATH", "./data/deltacrypto.db"),
		// firstEnv(...)：多个候选变量名里取第一个非空的（PROXY_URL > HTTPS_PROXY > https_proxy）。
		ProxyURL: firstEnv("PROXY_URL", "HTTPS_PROXY", "https_proxy"),
		// 鉴权令牌：没设置就是空串 = 不鉴权（localhost 默认）。
		AuthToken: getEnv("AUTH_TOKEN", ""),
	}
}

// firstEnv 依次尝试多个环境变量名，返回第一个非空的。
// “keys ...string” 是【可变参数】：调用方想传几个字符串就传几个，
// 函数内部 keys 自动变成一个 []string（字符串切片）。
func firstEnv(keys ...string) string {
	// “for ... range 切片” 是遍历容器（数组/切片/map）的循环：
	// 每次迭代返回两个值：下标、元素。这里用 _ 丢弃下标，k 接住每个元素。
	for _, k := range keys {
		// “if v := os.Getenv(k); v != ""” 是【if 带初始化语句】：
		// 先执行 os.Getenv(k) 把结果赋给 v，再判断 v 非空。
		// v 只在 if 块内有效（作用域被限制），不会污染外层——这是 Go 常用惯用法。
		if v := os.Getenv(k); v != "" {
			return v // 找到第一个非空就返回
		}
	}
	return "" // 全都不存在，返回空串（调用方当“没配置”处理）
}

// getEnv 读字符串环境变量；没设置时返回 fallback（默认值）。
// 为什么抽成这个小函数？—— 避免每个调用点都写一遍“os.Getenv + 判断 + 兜底”三行样板。
func getEnv(key, fallback string) string {
	// 又是这个“if 带初始化语句”的惯用法：先执行 os.Getenv(key)，把返回值赋给 v，
	// 然后判断 v 非空。这个写法的好处是 v 只在 if 块内有效（不会污染函数其它地方）。
	if v := os.Getenv(key); v != "" { // 取到非空值
		return v // 用它
	}
	return fallback // 没取到，回退默认值
}
