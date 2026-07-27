# DeltaCrypto · 超低频资金费率 + 基差套利工具

一个**个人使用**的量化小工具：在 **Binance 做空永续合约**、在 **Gate 做多现货**，赚取资金费率与基差收敛的收益。

按需求定位：**代码简单易读 > 性能**。单进程 + Goroutine，无微服务、无 JWT、前端简洁、本机 localhost 使用。

- 策略本质：资金费率为正时，**空头收资金费**；合约相对现货有溢价（基差），到期/收敛时基差部分也是利润。
- 运行形态：一个 Go 进程，内含 2 个 goroutine（HTTP 服务 + 自动交易循环），数据全部落本地 SQLite。

---

## 目录

1. [快速开始](#1-快速开始)
2. [整体架构图](#2-整体架构图)
3. [模块调用依赖关系](#3-模块调用依赖关系)
4. [自动交易流程图](#4-自动交易流程图)
5. [目录结构与脚手架解释](#5-目录结构与脚手架解释)
6. [七大模块说明](#6-七大模块说明)
7. [数据库表结构](#7-数据库表结构)
8. [RESTful API 一览](#8-restful-api-一览)
9. [全部可调参数](#9-全部可调参数)
10. [典型使用流程](#10-典型使用流程)
11. [设计决策与取舍](#11-设计决策与取舍)
12. [风险提示](#12-风险提示)

---

## 1. 快速开始

### 方式 A：Docker（推荐）

```bash
# 构建镜像（ccxt SDK 较大，首次编译约几分钟，请给 Docker Desktop ≥ 4GB 内存）
docker build -t deltacrypto:latest .

# 运行（数据存到 named volume，重启不丢）
docker run -d --name deltacrypto -p 8080:8080 -v deltacrypto-data:/app/data deltacrypto:latest

# 或一条命令（等价）
docker compose up -d
```

浏览器打开 <http://localhost:8080>。

### 方式 B：本地裸跑

```bash
# 1. 构建前端（产物输出到 web/static，后端自动托管）
cd web/frontend && npm ci && npm run build && cd ../..

# 2. 编译并运行后端
go build -o deltacrypto ./cmd/server
./deltacrypto          # Windows: deltacrypto.exe
# 浏览器打开 http://127.0.0.1:8080
```

### 前端开发模式（改前端时）

```bash
cd web/frontend
npm run dev            # Vite 开发服务器 http://127.0.0.1:5173，/api 已代理到 8080 后端
# 改完 npm run build 重新产出静态文件
```

### 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LISTEN_ADDR` | `127.0.0.1:8080`（容器内 `0.0.0.0:8080`） | HTTP 监听地址 |
| `DB_PATH` | `./data/deltacrypto.db`（容器内 `/app/data/...`） | SQLite 文件路径 |

---

## 2. 整体架构图

```
                        ┌──────────────────────────────────────────┐
                        │              浏览器（前端）               │
                        │ React+TS+Vite 像素风 SPA（8 个页签）      │
                        │ 构建产物由 Go 同源托管 web/static          │
                        └────────────────┬─────────────────────────┘
                                         │ 同源 RESTful API（无鉴权，localhost）
                                         ▼
┌────────────────────────────────────────────────────────────────────────────┐
│                        单进程 Go 程序（deltacrypto）                        │
│                                                                            │
│  goroutine-1: HTTP 服务（internal/httpserver）                              │
│     │ 仅服务前端；后端模块之间不走 HTTP，直接包内函数调用                      │
│     ▼                                                                      │
│  ┌────────────────────────── 业务模块层 ──────────────────────────┐        │
│  │ ①apiconfig ②market ③trade ④account ⑤alert ⑥autotrade ⑦settings│       │
│  └──────┬───────────┬──────────┬─────────┬────────┬───────────────┘        │
│         │           │          │         │        │                        │
│         ▼           ▼          ▼         ▼        ▼                        │
│  ┌─────────────┐ ┌────────────────────┐ ┌──────────────┐                  │
│  │ exchange.Hub│ │ database (SQLite)  │ │ notify(邮件) │                  │
│  │ 交易所抽象层 │ │ modernc.org/sqlite │ │ net/smtp SSL │                  │
│  └──────┬──────┘ └────────────────────┘ └──────────────┘                  │
│         │                                                                  │
│  goroutine-2: ⑥autotrade.RunLoop（每 N 秒一轮：预警→卖出→买入→邮件）         │
└─────────┼──────────────────────────────────────────────────────────────────┘
          │ ccxt (github.com/ccxt/ccxt/go/v4)，HTTPS REST
          ▼
   ┌─────────────┐          ┌─────────────┐
   │  Binance    │          │    Gate     │
   │ USDT 永续合约│          │  USDT 现货  │
   │ （做空腿）   │          │ （做多腿）   │
   └─────────────┘          └─────────────┘
```

---

## 3. 模块调用依赖关系

> 需求文档约定：**模块既能被前端调用，也能被其他后端模块调用**。
> 前端 → 走 RESTful API；后端模块之间 → 直接 import 包调用（单进程，无需 HTTP）。

```
                 ┌─────────────┐
                 │  httpserver │  （HTTP 层，唯一面向前端的入口）
                 └──────┬──────┘
        ┌───────┬───────┼───────┬────────┬────────┐
        ▼       ▼       ▼       ▼        ▼        ▼
    apiconfig market trade  account   alert   settings
                  ▲    ▲  ▲    ▲  ▲      ▲  ▲      ▲
                  │    │  │    │  │      │  │      │
                  └──┐ │  │    │  │      │  │      │
                     ▼ ▼  ▼    ▼  │      │  │      │
                 ┌────────────┐   │      │  │      │
                 │ autotrade  │───┘──────┘──┘      │
                 │ (自动交易)  │  直接包调用：        │
                 └─────┬──────┘  market/trade/     │
                       │          account/alert/   │
                       │          settings         │
                       ▼
   底层依赖（被上面所有模块共享）：
   ┌─────────────┐   ┌──────────┐   ┌─────────┐
   │ exchange.Hub│   │ database │   │ notify  │
   │ (ccxt 封装) │   │ (SQLite) │   │ (邮件)  │
   └─────────────┘   └──────────┘   └─────────┘
```

**精确到包 import 的依赖**（箭头 = 依赖/调用）：

| 模块 | 依赖 |
|---|---|
| `apiconfig` | database, exchange |
| `market` | exchange, settings |
| `trade` | database, exchange, settings |
| `account` | exchange, settings, trade |
| `alert` | database, exchange, settings, account, trade, notify |
| `autotrade` | settings, market, trade, account, alert, notify |
| `httpserver` | 以上全部（装配 + 暴露 REST） |
| `main` | 以上全部（依赖注入根） |

> 依赖方向严格单向（无循环依赖）：`settings/exchange/database/notify` 在最底层；
> `trade` 是“写持仓/写日志”的中心节点；`autotrade` 是最上层编排者。

---

## 4. 自动交易流程图

每 N 秒（`loop_interval_sec`，默认 15）执行一轮：

```
                    ┌─────────────────────────┐
                    │  读取设置：开关是否打开？  │
                    └───────────┬─────────────┘
                                │ 是
                                ▼
                    ┌─────────────────────────┐
                    │ ⑤预警模块 CheckAll()     │
                    │ 费率反转 / ADL / 爆仓 /   │
                    │ 资金平衡 → 写库+发邮件    │
                    └───────────┬─────────────┘
                                ▼
        ┌───────────── 阶段 1：卖出阶段 ─────────────┐
        │ 对每一个持仓币对，取当前资金费率 rate：      │
        │                                            │
        │  rate < 0 或有 ADL 预警 ──► 【fast sell】   │
        │      一股脑全部平仓，不看基差                │
        │                                            │
        │  rate > 0.01%（阈值可配）──► 【skip sell】  │
        │      持有有利可图，不卖                      │
        │                                            │
        │  0 ≤ rate ≤ 0.01% ──────► 【slow sell】    │
        │      若 当前基差 < 买入基差 → 平仓（顺带     │
        │      基差套利）；否则跳过，继续观望           │
        └───────────────────┬────────────────────────┘
                            ▼
              有卖出？ ──是──► 重新拉取账户与行情
                 │否           （否则行情足够新，不重拉）
                 ▼
        ┌───────────── 阶段 2：买入阶段 ─────────────┐
        │ 购买力 = min(合约账户×杠杆, 现货账户)        │
        │                                            │
        │  购买力 < 一组 50U ──► 【skip buy】留着现金  │
        │  行情列表为空     ──► 【skip buy】留着现金   │
        │                                            │
        │  否则 ──► 【scatter buy】                  │
        │   取行情列表前 n 个（n = min(3, 候选数,     │
        │   现金够买组数)），排除已持仓币对：           │
        │     第 i 个标的买 50U；                     │
        │     余额 < 50U 则上取整（有多少买多少）；     │
        │     没钱则跳过                              │
        └───────────────────┬────────────────────────┘
                            ▼
              本轮动作写日志入库（trade_log）
              有买卖动作 → 发邮件通知用户
                            ▼
                   sleep N 秒，进入下一轮
```

**单次下单内部（交易模块③）**：每组 50U 再拆成 5U 一笔的原子单，逐轮执行：

```
买入一组 50U：
  round = 1..10：
    量 = min(5U, 剩余)   ← 若执行后剩余 < 5U（粉尘），一并带走
    ① 现货腿：gate 市价买入   （优先腿）
    ② 合约腿：binance 市价开空 （对冲腿）
       └─ 失败 → 回滚现货腿（卖出刚买的币），消除净敞口
    每轮后 sleep 1s 并刷新价格（“牛吃草，吃几口抬头看一眼”）
```

---

## 5. 目录结构与脚手架解释

```
myProject/
├── cmd/
│   └── server/
│       └── main.go                  # 【入口】装配全部模块（依赖注入根）：
│                                    #   开 DB → settings → exchange.Hub → 7 个模块
│                                    #   → go autotrade.RunLoop() → 起 HTTP 服务
│
├── internal/                        # Go 的 internal 约定：包外不可见，保护内部实现
│   ├── config/
│   │   └── config.go                # 环境变量读取（监听地址、DB 路径），带默认值
│   │
│   ├── model/
│   │   └── model.go                 # 全部共用数据结构（凭证/行情/交易/持仓/账户/预警/日志）
│   │                                #   集中一处，避免模块间循环依赖
│   │
│   ├── database/
│   │   └── db.go                    # SQLite 打开 + 建表（5 张表），纯 Go 驱动免 CGO
│   │
│   ├── exchange/
│   │   └── exchange.go              # 【交易所抽象层】ccxt 统一封装：
│   │                                #   - Exchange 类型：行情/费率/余额/持仓/下单/ADL
│   │                                #   - Hub 管理器：现货腿(gate)+合约腿(binance)，
│   │                                #     凭证变更后 Reload() 热更新连接
│   │                                #   - 更换交易所只需改 newCcxtClient 的一个 case
│   │
│   ├── notify/
│   │   └── mail.go                  # 邮件发送（net/smtp + SSL 465），预警/通知用
│   │
│   ├── service/                     # 【业务模块层】对应需求文档的 7 大模块
│   │   ├── apiconfig/apiconfig.go   # ① API 配置：保存凭证、测试连通性/权限
│   │   ├── market/market.go         # ② 行情：四重约束过滤出“待买入列表”
│   │   ├── trade/trade.go           # ③ 交易：双腿对冲执行器（拆单/粉尘/回滚/落库）
│   │   ├── account/account.go       # ④ 账户：双所资金/聚合/购买力/持仓/运行时长
│   │   ├── alert/alert.go           # ⑤ 预警：费率反转/ADL/爆仓/资金平衡 → 邮件
│   │   ├── autotrade/autotrade.go   # ⑥ 自动交易：调度循环（卖出阶段→买入阶段）
│   │   └── settings/settings.go     # ⑦ 设置：全部参数的 key/默认值/说明，读写库
│   │
│   └── httpserver/
│       └── server.go                # HTTP 层：标准库手写路由，RESTful API + 静态文件
│
├── web/
│   ├── frontend/                    # 【前端源码】React 18 + TypeScript + Vite
│   │   ├── src/
│   │   │   ├── main.tsx             # 入口：挂载 React + 引入像素字体（fontsource 本地打包）
│   │   │   ├── App.tsx              # 主框架：像素标题牌 + 8 页签导航
│   │   │   ├── api.ts               # API 封装 + 全部 TS 类型定义（与后端 model 对应）
│   │   │   ├── styles/pixel.css     # 【像素风设计系统】清新复古 NES 风：
│   │   │   │                        #   阶梯角（双层 clip-path）、硬阴影、像素字体、
│   │   │   │                        #   天蓝背景 + CSS 像素云/星星
│   │   │   ├── components/Pixel.tsx # 像素组件库：按钮/卡片/输入框/表格/徽章/弹窗/天空装饰
│   │   │   └── pages/               # 8 个页面：API配置/行情/交易/账户/预警/自动交易/设置/日志
│   │   ├── package.json / vite.config.ts / tsconfig.json / index.html
│   │   └── （npm run build 产物输出到 ../static）
│   └── static/                      # 【构建产物】Go 后端直接托管，勿手改
│
├── Dockerfile                       # 三阶段构建：node 构建前端 → golang 编译 → alpine 运行（≈54MB）
├── docker-compose.yml               # 可选编排（等价于 docker run）
├── .dockerignore                    # 排除数据/文档/node_modules，减小构建上下文
├── go.mod / go.sum                  # Go 依赖清单（核心依赖：ccxt、modernc.org/sqlite）
└── README.md                        # 本文档
```

**脚手架的关键设计**：

1. **`cmd/` 与 `internal/` 分离**：Go 社区标准布局。`cmd` 只放入口装配代码；所有实现都在 `internal`（外部项目无法 import，天然防误用）。
2. **`model` 包独立**：所有数据结构集中定义，任何模块都能引用，**这是避免循环依赖的关键手法**。
3. **依赖注入（手工）**：`main.go` 里按依赖顺序 `New()` 出所有模块，层层传入。没有用任何 DI 框架——个人工具，30 行装配代码比框架更直观。
4. **模块边界 = 包边界**：每个模块一个包，`New(...)` 返回 `*Service`，对外只暴露方法。后端模块间调用就是普通的 Go 函数调用（如 `autotrade` 调 `market.Candidates()`）。
5. **每个文件头部都有包级中文注释**：说明该模块对应需求文档的哪几条要点，方便对照。
6. **前后端分离但同源部署**：前端 React+TS 源码在 `web/frontend`，Vite 构建产物输出到 `web/static`，Go 直接托管——无跨域、无 JWT、无独立前端服务，Docker 里用一个三阶段构建串起整条链路。

---

## 6. 七大模块说明

### ① API 配置模块（`apiconfig`）
- 前端提交 `交易所 + Key + Secret` → 存 SQLite（每个所保留最新一条，Secret 在列表接口中脱敏）。
- 「测试连接」：临时建连，依次验证 **公共连通性**（FetchTime）与 **私有权限**（FetchBalance），结果返回前端。
- 保存后自动 `hub.Reload()` **热更新交易所连接**，不用重启。

### ② 行情模块（`market`）
- 四重约束全部通过才进入“待买入列表”：
  1. 24H 合约成交额 > `min_quote_volume_24h`（默认 50K）—— 先粗筛，最便宜；
  2. 与 Gate 现货取**交集**（两边都有的币才能对冲）；
  3. 最近 N 次费率**趋势上升**（前一半均值 < 后一半均值）且 **均值 > 0.05%**（8 并发拉历史费率加速）；
  4. **基差 > 0.1%**（合约比现货贵）。
- 输出按当前费率降序，前端表格与自动交易买入共用此列表。

### ③ 交易模块（`trade`）
- **双腿对冲执行器**：现货腿（优先腿，gate）+ 合约腿（对冲腿，binance），市价单。
- 总量拆成原子单位逐轮执行；**粉尘处理**（剩余 < 阈值一并带走）；**轮间停顿 1s 并刷新价格**（牛吃草）。
- 合约腿失败 → **自动回滚现货腿**，消除净敞口，并写 error 日志。
- 平仓单带 `reduceOnly`，防止误开反向仓。
- 建仓成功写 `hedge_position`（记录入场基差，slow sell 要用）；平仓置 `closed`。
- 下单引擎不自研：底层就是 ccxt 的 `CreateOrder`，本模块只做双腿协调。

### ④ 账户信息模块（`account`）
- 双所 USDT 资金（可用/冻结/总额）、聚合总资金。
- **购买力 = min(合约账户 × 杠杆, 现货账户)**（需求文档明确公式）。
- 当前对冲持仓（数据库）+ 合约实时持仓（含强平价）+ 运行时长。

### ⑤ 预警模块（`alert`）
- 每轮检查（也可前端手动触发）：
  - **费率反转**：持仓币对当前费率 < 0（critical）；
  - **ADL**：币安 ADL 排名 ≥ 4/5（critical）；
  - **爆仓**：标记价距强平价 < 10%（critical）；
  - **资金平衡**：现货:合约 偏离 1:4 超过 15%（warning，提示手动平衡，机器人不自动调仓）。
- 触发 → 写 `alert_log` + 发邮件；**4 小时去重窗口**防止邮件轰炸。

### ⑥ 自动交易模块（`autotrade`）
- 常驻 goroutine，每 `loop_interval_sec` 秒一轮；开关 `auto_trade_enabled` 关闭时 3 秒轻轮询。
- 严格按需求实现 **卖出三态**（skip/slow/fast sell）与 **买入两态**（skip/scatter buy），详见[流程图](#4-自动交易流程图)。
- 有卖出才重新拉行情；每轮动作写日志；有买卖动作发邮件汇报。

### ⑦ 设置模块（`settings`）
- 全部参数（20 项）集中声明 key/默认值/中文说明，启动时写入数据库。
- 前端设置页**自动渲染全部参数**（新增参数只改一处 `AllParams`），改完即时生效，无需重启。

---

## 7. 数据库表结构

| 表 | 用途 | 关键字段 |
|---|---|---|
| `exchange_api` | 交易所凭证（模块①） | exchange, label, api_key, api_secret |
| `settings` | 全部参数键值对（模块⑦） | key, value |
| `hedge_position` | 对冲持仓（模块③写入，④⑤⑥读取） | symbol, spot/swap_amount, 双边均价, entry_basis_pct, status(open/closed) |
| `trade_log` | 操作日志（③⑤⑥写入，前端日志页） | module, level, action, symbol, message |
| `alert_log` | 预警记录（模块⑤） | type, symbol, level, message, mail_sent |

> SQLite 开了 WAL 模式；单连接写入（`SetMaxOpenConns(1)`）避免文件锁冲突。

---

## 8. RESTful API 一览

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/config/apis` | 凭证列表（Secret 脱敏） |
| POST | `/api/config/apis` | 保存凭证（热更新连接） |
| DELETE | `/api/config/apis/{exchange}` | 删除凭证 |
| POST | `/api/config/test` | 测试连通性/权限 |
| GET | `/api/market/candidates` | 待买入列表（四重约束过滤后） |
| POST | `/api/trade/open` | 手动建仓 `{symbol, total_usdt?, atom_usdt?}` |
| POST | `/api/trade/close` | 手动平仓 `{symbol, position_id?}` |
| GET | `/api/trade/logs` | 最近 200 条日志 |
| GET | `/api/account/overview` | 账户总览 |
| GET | `/api/alert/records` | 预警记录 |
| POST | `/api/alert/check` | 立即检查一轮预警 |
| GET | `/api/autotrade/status` | 自动交易状态 |
| POST | `/api/autotrade/run` | 立即执行一轮 |
| GET/POST | `/api/settings` | 读取/批量保存全部参数 |

---

## 9. 全部可调参数

（设置页可见，括号内为默认值）

| 参数 | 默认 | 用途 |
|---|---|---|
| `funding_count` | 5 | 最近 N 次费率用于趋势/均值 |
| `min_basis_pct` | 0.1 | 基差下限（%） |
| `min_funding_avg_pct` | 0.05 | N 次费率均值下限（%） |
| `min_quote_volume_24h` | 50000 | 流通量下限（USDT） |
| `hold_sell_threshold_pct` | 0.01 | 持有不卖阈值（%） |
| `group_size_usdt` | 50 | 组容量（U） |
| `atom_size_usdt` | 5 | 原子单位（U） |
| `dust_usdt` | 5 | 粉尘阈值（U） |
| `max_buy_pairs` | 3 | 最多分散买入对数 |
| `loop_interval_sec` | 15 | 自动交易间隔（秒） |
| `auto_trade_enabled` | 0 | 自动交易总开关 |
| `leverage` | 4 | 合约杠杆 |
| `balance_ratio` | 4 | 资金分配 合约:现货=1:N |
| `balance_warn_pct` | 15 | 资金平衡预警阈值（%） |
| `smtp_*` / `mail_*` | — | 邮件通知配置（QQ/163 邮箱用 SMTP 授权码） |

---

## 10. 典型使用流程

```
1. 启动容器/程序，浏览器打开 http://localhost:8080
2. 「API配置」：分别保存 gate 与 binance 的 Key → 点“测试连接”确认 ✔
   （建议：交易所后台绑定 IP 白名单；只开交易权限，不开提币）
3. 「设置」：按需调整参数；填好 SMTP 邮箱（预警/通知靠它）
4. 「行情」：点刷新，查看通过全部约束的待买入列表（空表 = 当前无优质标的，正常）
5. 「交易」：可先手动小额建仓一笔，验证两腿成交与持仓记录
6. 「设置」：auto_trade_enabled 改为 1 → 「自动交易」页观察每轮状态与摘要
7. 「预警」「日志」「账户」页随时查看系统行为
```

---

## 11. 设计决策与取舍

| 决策 | 理由（对应需求文档） |
|---|---|
| 单进程 + 2 goroutine | “个人开发个人使用，不要微服务” |
| 无 JWT、监听 localhost | “前端无需跨域，简单本机即可”（容器环境由 Docker 网络隔离） |
| SQLite + 纯 Go 驱动 | 零运维、免 CGO、镜像小、备份就是拷文件 |
| 市价单而非 Maker 前 3 档追价 | “延迟几秒都能接受”；市价单双腿秒级成对成交，**净敞口时间最短**，代码量少一个数量级 |
| 一个持仓 = 一组 50U | 让“卖出 50U 下取整”天然等价于“平掉一个持仓”，slow/fast sell 逻辑大幅简化 |
| ccxt 而非自研 REST 封装 | 需求指定；交易所细节（签名/精度/参数）由 ccxt 兜底 |
| 交易所抽象层 + Hub | “连接要抽象化，方便更换交易所”：换所只改 `newCcxtClient` 一处 |
| 后端模块直接包调用 | 需求原文约定；无 HTTP 开销，类型安全 |
| 4 小时预警去重 | 防止 7×24 运行时邮件轰炸 |

---

## 12. 风险提示

- **本工具不构成投资建议**，量化套利存在：费率反转、基差走扩、强平、ADL、交易所宕机、API 故障等风险。
- 首次使用务必**小资金试跑**，确认两腿成交正常后再放大。
- API Key 权限最小化：只开“现货/合约交易”，**绝不开提币**，并绑定 IP 白名单。
- 凭证以明文存于本机 SQLite（个人本机场景）；请勿把数据库文件泄露给他人。
- 资金平衡需要人工转账调仓（预警会提醒），机器人不自动转账。
