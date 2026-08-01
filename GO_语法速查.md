# 本项目用到的 Go 语法速查表（新手对照用）

> 读代码时遇到看不懂的语法,来这张表查。每一条都写了**它是什么、怎么写、在本项目哪里出现**。
> 所有注释里都标注了【阅读顺序 NN】,按编号读;这张表是"语法字典",随查随用。

## 1. 变量与声明

| 语法 | 意思 | 项目例子 |
|---|---|---|
| `:=` | 声明新变量 + 赋值,类型自动推断 | `cfg := config.Load()` |
| `=` | 给已存在的变量赋值 | `proxyURL = url` |
| `var x 类型` | 声明变量(带类型),不赋值 = 零值 | `var out []string` |
| `var x = 值` | 声明变量(类型推断) | `var savedPositionID = positionID` |
| `const` | 常量,编译期确定,不可改 | `const dedupWindow = 4 * time.Hour` |
| `_` | 空标识符:丢弃不用的返回值 | `_, err := ex.TestPublic()` |
| `int64(x)` `int(x)` `float64(x)` | 类型转换:目标类型(值) | `int64(lev)`, `int(math.Floor(...))` |

## 2. 函数

| 语法 | 意思 | 项目例子 |
|---|---|---|
| `func 名(参数) 返回值` | 函数定义 | `func Load() *Config` |
| 多返回值 | 一个函数返回多个值,最后一个常是 error | `db, err := database.Open(...)` |
| 命名返回值 | 返回值提前起名,函数体可直接赋值 | `func (...) (free, used, total float64, err error)` |
| 可变参数 `...T` | 任意多个参数 | `func firstEnv(keys ...string) string` |
| `opts...` | 把切片展开成多个参数 | `e.client.FetchFundingHistory(opts...)` |
| 方法接收者 | `func (s *Service) Xxx()` 把函数挂到类型上 | `(s *Service) Get() string` |
| 值接收者 vs 指针接收者 | 值=操作副本;指针=操作原对象 | `(m MailConfig)` vs `(e *Exchange)` |

## 3. 控制流

| 语法 | 意思 | 项目例子 |
|---|---|---|
| `if 条件 { }` | 判断 | `if err != nil` |
| `if x := ...; 条件 { }` | if 带初始化:先执行再判断,x 只在 if 内有效 | `if v := os.Getenv(k); v != ""` |
| `for 条件 { }` | 相当于 while | `for remainingUSDT > 0` |
| `for {}` | 无限循环 | 自动交易主循环 |
| `for i := 0; i < n; i++` | 经典计数循环 | 分散买入 |
| `for ... range` | 遍历切片/map/channel | `for _, p := range positions` |
| `continue` | 跳过本轮,进下一轮 | 跳过不处理的持仓 |
| `break` | 跳出整个循环 | 突破上限停止 |
| `switch 值 { case ... }` | 分支,case 自动跳出不用 break | 按交易所 ID 分发 |
| `switch { case 条件: }` | 无条件 switch,纯按条件分发 | 卖出三态判断 |

## 4. 数据结构

| 语法 | 意思 | 项目例子 |
|---|---|---|
| `type T struct { }` | 定义结构体(字段集合) | `type Config struct` |
| `T{字段: 值}` | 结构体字面量 | `&Config{ListenAddr: ...}` |
| 匿名结构体 | 一次性使用的结构体,不命名 | `var req struct{ Symbol string `json:"symbol"` }` |
| `[]T` | 切片(动态数组) | `[]string`, `[]model.AlertRecord` |
| `make([]T, 0)` | 空切片(JSON 输出 []) | 所有列表函数 |
| `append(s, x)` | 追加元素,必须赋值回去 | `out = append(out, r)` |
| `s[a:b]` | 切片切分 | `rates[:mid]`(前一半), `swap[:i]` |
| `map[K]V` | 字典 | `map[string]any{...}` |
| `map[string]struct{}` | 当"集合"用(只看在不在) | `spotSet[sym] = struct{}{}` |
| `v, ok := m[k]` | 取 map 值 + 判断键是否存在 | `if v, ok := bal.Free["USDT"]; ok` |
| `*T` `&x` | 指针 / 取地址 | `*sql.DB`, `&a.ID` |
| `struct{}` | 空结构体,占 0 字节 | 信号量 `sem <- struct{}{}` |

## 5. 错误处理

| 语法 | 意思 | 项目例子 |
|---|---|---|
| `if err != nil` | 出错判断(Go 没有 try/catch) | 到处都是 |
| `fmt.Errorf("...: %w", err)` | 包装错误,保留底层链条 | `fmt.Errorf("建表失败: %w", err)` |
| `errors.New("...")` | 构造一个普通错误 | `errors.New("盘口为空")` |
| `return nil, err` | 出错时返回零值 + 错误 | 列表查询 |

## 6. 并发(Goroutine)

| 语法 | 意思 | 项目例子 |
|---|---|---|
| `go 函数()` | 启动轻量线程 | `go autotradeSvc.RunLoop()` |
| `make(chan T, N)` | 通道(有缓冲) | `results := make(chan frResult, len(both))` |
| `ch <- x` | 发送 | `results <- frResult{...}` |
| `<-ch` / `for x := range ch` | 接收 / 收完自动退出 | `for r := range results` |
| `close(ch)` | 关闭通道 | `close(results)` |
| `sync.WaitGroup` | 等一组 goroutine 干完 | `wg.Add(1)/wg.Done()/wg.Wait()` |
| 信号量 `sem <- struct{}{}` | 限流,同时最多 N 个 | 行情并发 8 个 |
| `sync.Mutex` / `sync.RWMutex` | 互斥锁 / 读写锁 | `h.mu.RLock()/RUnlock()`, `s.mu.Lock()/Unlock()` |
| goroutine 闭包坑 | 循环变量必须按值传参 | `go func(base string){...}(base)` |

## 7. defer

| 语法 | 意思 | 项目例子 |
|---|---|---|
| `defer 动作()` | 函数返回前【一定】执行 | `defer db.Close()`、`defer rows.Close()`、`defer h.mu.RUnlock()`、`defer wg.Done()` |
| 为什么要用它 | 无论从哪一行 return 都保证执行 | 打开资源后立刻 defer 关闭 |

## 8. 字符串 / 数字

| 语法 | 意思 | 项目例子 |
|---|---|---|
| `"a" + "b"` | 字符串拼接 | `base + ":USDT"` |
| `strings.Contains(s, sub)` | 是否含子串 | `strings.Contains(base, ":")` |
| `strings.Index(s, sub)` | 找子串下标 | `strings.Index(swap, ":")` |
| `strings.TrimPrefix(s, pre)` | 去前缀 | `strings.TrimPrefix(r.URL.Path, ...)` |
| `strings.Join(slice, sep)` | 拼成一串 | `strings.Join(errs, "；")` |
| `strings.Builder` | 循环拼字符串的高效工具 | 拼邮件正文、预警摘要 |
| `strconv.Atoi(s)` | 字符串→int | `strconv.Atoi(s.Get(key))` |
| `strconv.ParseFloat(s, 64)` | 字符串→float64 | `strconv.ParseFloat(s, 64)` |
| `len(s)` / `cap(s)` | 长度 / 容量 | `len(symbols)` |

## 9. 时间

| 语法 | 意思 | 项目例子 |
|---|---|---|
| `time.Now()` | 当前时间 | `roundStart := time.Now()` |
| `time.Since(t)` | 从 t 到现在过了多久 | `time.Since(openedAt)` |
| `time.Duration` | 本质是 int64 纳秒数 | `1 * time.Second`, `time.Duration(interval) * time.Second` |
| `time.Sleep(d)` | 睡 d 这么久 | 拆单轮间停顿 |
| `.Format("2006-01-02 15:04:05")` | ⚠️ Go 固定参考时间模板 | 详情页、邮件时间 |
| `time.UnixMilli(ms)` | 毫秒时间戳→时间 | 资金费流水 |
| `.UTC()` / `.Local()` | 时区转换 | 资金费结算点 |
| `.Add(8 * time.Hour)` | 加一段时长 | 找下一个结算点 |
| `.Hours()` / `.Minutes()` | Duration→小时/分钟数 | 年化计算 |

## 10. fmt 格式符(在格式化字符串里用)

| 格式符 | 意思 | 项目例子 |
|---|---|---|
| `%s` | 字符串 | `%s` 币对 |
| `%d` | 整数 | 轮次、天数 |
| `%v` | 任意值的默认表示 | 错误、日志 |
| `%.2f` `%.4f` `%.6f` | 保留 N 位小数的浮点数 | 金额、费率 |
| `%w` | 包装错误(只能用于 fmt.Errorf) | 错误链 |
| `%%` | 输出一个【字面百分号】 | 费率 0.05% |
| `\n` | 换行 | 邮件正文 |

## 11. 标准库包(本项目用到的)

| 包 | 用途 | 代表函数 |
|---|---|---|
| `fmt` | 格式化 | Sprintf / Errorf / Sscanf |
| `os` | 环境变量/文件 | Getenv / MkdirAll |
| `log` | 日志 | Printf / Println / Fatalf |
| `time` | 时间 | Now / Sleep / Since |
| `strings` | 字符串 | Contains / Join / Builder |
| `strconv` | 字符串↔数字 | Atoi / ParseFloat |
| `math` | 数学 | Min / Abs / Floor / Max |
| `sort` | 排序 | Slice |
| `encoding/json` | JSON | NewDecoder / NewEncoder / tag |
| `net/http` | HTTP | NewServeMux / ListenAndServe / FileServer |
| `database/sql` | 数据库 | Query / QueryRow / Exec / Scan |
| `net/smtp` `crypto/tls` | 发邮件 | PlainAuth / DialWithDialer |

## 12. SQL(database/sql 用的)

| 语法 | 意思 | 项目例子 |
|---|---|---|
| `?` | 参数占位符(防注入,别拼接字符串) | `WHERE key = ?` |
| `rows.Next()` | 游标有没有下一行 | 列表查询 |
| `rows.Scan(&x)` | 把当前行列填进变量(传指针) | `rows.Scan(&a.ID, ...)` |
| `rows.Err()` | 遍历中途是否出错 | `return out, rows.Err()` |
| `INSERT OR IGNORE` | 存在就忽略(去重) | 资金费流水 |
| `ON CONFLICT(key) DO UPDATE` | 存在则更新(UPSERT) | 设置保存 |
| `COALESCE(x, 0)` | 空值补 0 | SUM(fee) |
| `CURRENT_TIMESTAMP` | 数据库当前时间 | created_at 默认值 |
| `UNIQUE(...)` | 唯一约束(物理去重) | 资金费流水表 |

## 13. HTTP(本项目 web 接口用的)

| 语法 | 意思 | 项目例子 |
|---|---|---|
| `mux.HandleFunc(路径, 处理函数)` | 注册路由 | 所有 /api/* |
| `r.Method` | 请求方法(GET/POST/DELETE) | `switch r.Method` |
| `r.URL.Query().Get("x")` | 取 URL 参数 ?x= | `?symbol=BTC/USDT` |
| `r.Body` | 请求体(JSON 文本流) | `json.NewDecoder(r.Body)` |
| `w.WriteHeader(状态码)` | 设置响应状态码 | 405/400/500 |
| `w.Header().Set(...)` | 设置响应头 | Content-Type |
| `w.Write` | 写响应体 | json.Encode 内部用 |

---

> ⚠️ 最值得注意的三个"反直觉坑":① `.Format("2006-01-02 15:04:05")` 是固定参考时间模板;② 想要输出百分号要写 `%%`;③ `ccxt.MarketInterface` 名字带 Interface 其实是结构体。
