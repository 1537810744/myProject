// 【阅读顺序 22】API 层：前端与 Go 后端的全部对话都在这里。
// 阅读目的：上半部分是 TS 类型（与后端 model 包一一对应），下半部分是
// 按模块分组的请求方法（api.xxx）。后端统一返回 { data } 或 { error }；
// requestList 把空表场景后端返回的 null 统一兜底为空数组，防止 .map 崩溃。

// ---------- 类型定义（与后端 model 包对应） ----------

export interface ExchangeAPI {
  id: number
  exchange: string
  label: string
  api_key: string
  api_secret: string
  created_at: string
}

export interface APITestResult {
  exchange: string
  connected: boolean
  permission: boolean
  message: string
}

export interface MarketCandidate {
  symbol: string
  swap_exchange: string
  spot_exchange: string
  swap_price: number
  spot_price: number
  basis_pct: number
  funding_rate: number
  funding_avg_pct: number
  funding_rates: number[]
  annualized_pct: number
  quote_volume_24h: number
  direction: string
  updated_at: string
}

export interface LegResult {
  exchange: string
  market_type: string
  side: string
  amount: number
  avg_price: number
  cost_usdt: number
  order_ids: string[]
}

export interface TradeResult {
  success: boolean
  message: string
  spot_leg?: LegResult
  swap_leg?: LegResult
  timestamp: string
}

export interface HedgePosition {
  id: number
  symbol: string
  spot_exchange: string
  swap_exchange: string
  spot_amount: number
  swap_amount: number
  spot_entry_price: number
  swap_entry_price: number
  entry_basis_pct: number
  status: string
  opened_at: string
}

export interface ExchangeBalance {
  exchange: string
  market_type: string
  usdt_free: number
  usdt_used: number
  usdt_total: number
}

export interface SwapPositionInfo {
  exchange: string
  symbol: string
  side: string
  contracts: number
  entry_price: number
  mark_price: number
  unrealized_pnl: number
  leverage: number
  liquidation_price: number
}

export interface AccountOverview {
  balances: ExchangeBalance[]
  total_usdt: number
  purchasing_power: number
  hedges: HedgePosition[]
  swap_positions: SwapPositionInfo[]
  running_since: string
}

export interface AlertRecord {
  id: number
  time: string
  type: string
  symbol: string
  level: string
  message: string
  mail_sent: boolean
}

export interface TradeLog {
  id: number
  time: string
  module: string
  level: string
  action: string
  symbol: string
  message: string
}

export interface SettingItem {
  key: string
  value: string
  default: string
  description: string
}

export interface AutoStatus {
  enabled: boolean
  last_run_at: string
  round_count: number
  last_summary: string
}

// ---------- 持仓详情（账户模块） ----------

export interface PositionFill {
  id: number
  position_id: number
  symbol: string
  exchange: string
  market_type: string
  side: string
  price: number
  amount: number
  cost_usdt: number
  fee: number
  fee_currency: string
  order_id: string
  maker: boolean
  traded_at: string
}

export interface FundingPaymentRecord {
  id: number
  exchange: string
  symbol: string
  amount: number
  income_at: string
}

export interface PositionDetailStats {
  swap_margin_used: number
  spot_cost_used: number
  basis_pnl: number
  funding_pnl: number
  net_profit: number
  fee_usdt: number
  yield_pct: number
  annualized_pct: number
  run_duration: string
  net_exposure: number
  next_funding_est: number
}

export interface PositionLegDetail {
  exchange: string
  amount: number
  avg_price: number
  mark_price: number
  value_usdt: number
  unrealized_pnl: number
  realized_pnl: number
  next_funding_pct: number
  next_settle_at: string
  last_sync_at: string
}

export interface PositionDetail {
  symbol: string
  status: string
  stats: PositionDetailStats
  swap_leg: PositionLegDetail
  spot_leg: PositionLegDetail
  exposure: { swap_amount: number; spot_amount: number; net_exposure: number }
}

export interface ProfitPoint {
  time: string
  net_profit: number
  basis_pnl: number
  funding_cum: number
  fee_cum: number
}

// ---------- 请求封装 ----------

async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  })
  const json = await res.json()
  if (json.error) throw new Error(json.error)
  return json.data as T
}

// 数组兜底：后端在空表时返回 {data:null}，统一转成空数组，避免前端 .map 崩溃
async function requestList<T>(path: string): Promise<T[]> {
  const data = await request<T[] | null>(path)
  return data ?? []
}

// ---------- 模块 1：API 配置 ----------
export const api = {
  listAPIs: () => requestList<ExchangeAPI>('/api/config/apis'),
  saveAPI: (payload: { exchange: string; label: string; api_key: string; api_secret: string }) =>
    request<{ status: string }>('/api/config/apis', 'POST', payload),
  deleteAPI: (exchange: string) => request<{ status: string }>(`/api/config/apis/${exchange}`, 'DELETE'),
  testAPI: (payload: { exchange: string; role: string; api_key?: string; api_secret?: string }) =>
    request<APITestResult>('/api/config/test', 'POST', payload),

  // 模块 2：行情
  candidates: () => requestList<MarketCandidate>('/api/market/candidates'),

  // 模块 3：交易
  tradeOpen: (payload: { symbol: string; total_usdt?: number; atom_usdt?: number }) =>
    request<TradeResult>('/api/trade/open', 'POST', payload),
  tradeClose: (payload: { symbol: string; position_id?: number; total_usdt?: number }) =>
    request<TradeResult>('/api/trade/close', 'POST', payload),
  tradeLogs: () => requestList<TradeLog>('/api/trade/logs'),

  // 模块 4：账户
  overview: () => request<AccountOverview>('/api/account/overview'),
  positionDetail: (symbol: string) => request<PositionDetail>(`/api/position/detail?symbol=${encodeURIComponent(symbol)}`),
  positionFills: (symbol: string) => requestList<PositionFill>(`/api/position/fills?symbol=${encodeURIComponent(symbol)}`),
  positionFunding: (symbol: string) => requestList<FundingPaymentRecord>(`/api/position/funding?symbol=${encodeURIComponent(symbol)}`),
  positionProfit: (symbol: string) => requestList<ProfitPoint>(`/api/position/profit?symbol=${encodeURIComponent(symbol)}`),

  // 模块 5：预警
  alertRecords: () => requestList<AlertRecord>('/api/alert/records'),
  alertCheck: async () => (await request<AlertRecord[] | null>('/api/alert/check', 'POST')) ?? [],

  // 模块 6：自动交易
  autoStatus: () => request<AutoStatus>('/api/autotrade/status'),
  autoRun: () => request<{ status: string }>('/api/autotrade/run', 'POST'),

  // 模块 7：设置
  settings: () => requestList<SettingItem>('/api/settings'),
  saveSettings: (kv: Record<string, string>) => request<{ status: string }>('/api/settings', 'POST', kv),
}

// ---------- 格式化工具 ----------
export const fmt = (n: number | null | undefined, d = 4): string =>
  n === null || n === undefined ? '-' : Number(n).toFixed(d)

export const fmtTime = (s: string): string => {
  if (!s || s.startsWith('0001')) return '-'
  return s.replace('T', ' ').slice(0, 19)
}
