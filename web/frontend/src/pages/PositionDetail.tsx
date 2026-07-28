// 【阅读顺序 26】持仓详情页 —— 单个对冲币对的完整画像（对应《第一次更新》第 2 条）。
// 结构：顶部统计卡 → 双腿详情 → 敞口分析 → 页签（成交记录 / 资金费率流水 / 收益曲线）
import { useEffect, useState } from 'react'
import {
  api, PositionDetail as Detail, PositionFill, FundingPaymentRecord, ProfitPoint, fmt, fmtTime,
} from '../api'
import { PixelButton, PixelCard, PixelTable, StatCard, PixelBadge, PixelMessage, useMessage } from '../components/Pixel'

type TabKey = 'fills' | 'funding' | 'profit'

export default function PositionDetailPage({ symbol, onBack }: { symbol: string; onBack: () => void }) {
  const [detail, setDetail] = useState<Detail | null>(null)
  const [tab, setTab] = useState<TabKey>('fills')
  const [fills, setFills] = useState<PositionFill[]>([])
  const [funding, setFunding] = useState<FundingPaymentRecord[]>([])
  const [profit, setProfit] = useState<ProfitPoint[]>([])
  const [msg, showMsg] = useMessage()

  // 加载主详情（统计/双腿/敞口）
  const loadDetail = () =>
    api.positionDetail(symbol).then(setDetail).catch((e) => showMsg('err', '✘ 详情加载失败：' + e.message))

  useEffect(() => { loadDetail() }, [symbol])

  // 页签数据按需加载
  useEffect(() => {
    if (tab === 'fills') api.positionFills(symbol).then(setFills).catch(() => {})
    if (tab === 'funding') api.positionFunding(symbol).then(setFunding).catch(() => {})
    if (tab === 'profit') api.positionProfit(symbol).then(setProfit).catch(() => {})
  }, [tab, symbol])

  const s = detail?.stats
  const pnlColor = (n: number | undefined) => ((n ?? 0) >= 0 ? 'green' : 'red') as 'green' | 'red'

  return (
    <>
      {/* 标题栏：返回 + 币对 + 刷新 */}
      <div style={{ display: 'flex', gap: 10, alignItems: 'center', marginBottom: 12 }}>
        <PixelButton onClick={onBack}>◀ 返回</PixelButton>
        <span className="pbadge blue pixel-clip-sm" style={{ fontSize: 12, padding: '6px 14px' }}>
          {symbol} 费率套利详情
        </span>
        <PixelButton variant="primary" onClick={loadDetail}>🔄 刷新</PixelButton>
      </div>
      <PixelMessage kind={msg.kind} text={msg.text} />

      {/* ===== 第一排统计：资金占用与收益 ===== */}
      <div className="stat-grid" style={{ marginTop: 12 }}>
        <StatCard label="合约占用资金" value={`${fmt(s?.swap_margin_used ?? 0, 2)} U`} />
        <StatCard label="现货占用资金" value={`${fmt(s?.spot_cost_used ?? 0, 2)} U`} />
        <StatCard label="期现收益" value={fmt(s?.basis_pnl ?? 0, 4)} color={pnlColor(s?.basis_pnl)} />
        <StatCard label="费率收益" value={fmt(s?.funding_pnl ?? 0, 4)} color={pnlColor(s?.funding_pnl)} />
        <StatCard label="净收益" value={fmt(s?.net_profit ?? 0, 4)} color={pnlColor(s?.net_profit)} />
        <StatCard label="手续费(USDT)" value={fmt(s?.fee_usdt ?? 0, 4)} color="red" />
      </div>

      {/* ===== 第二排统计：比率与敞口 ===== */}
      <div className="stat-grid">
        <StatCard label="收益率" value={`${fmt(s?.yield_pct ?? 0, 4)}%`} color={pnlColor(s?.yield_pct)} />
        <StatCard label="年化收益率" value={`${fmt(s?.annualized_pct ?? 0, 2)}%`} color={pnlColor(s?.annualized_pct)} />
        <StatCard label="运行时长" value={s?.run_duration || '-'} />
        <StatCard label="敞口（币）" value={fmt(s?.net_exposure ?? 0, 4)} color={s?.net_exposure === 0 ? 'green' : 'red'} />
        <StatCard label="下次费率预估" value={fmt(s?.next_funding_est ?? 0, 4)} color={pnlColor(s?.next_funding_est)} />
      </div>

      {/* ===== 双腿详情 + 敞口分析 ===== */}
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 12, alignItems: 'stretch' }}>
        <LegCard title={`合约腿 - ${detail?.swap_leg.exchange || '-'}`} leg={detail?.swap_leg} isSwap />
        <LegCard title={`现货腿 - ${detail?.spot_leg.exchange || '-'}`} leg={detail?.spot_leg} isSwap={false} />
        <PixelCard title="敞口分析">
          <div className="kv"><span>合约持仓</span><b className="num">{fmt(detail?.exposure.swap_amount ?? 0, 4)}</b></div>
          <div className="kv"><span>现货持仓</span><b className="num">{fmt(detail?.exposure.spot_amount ?? 0, 4)}</b></div>
          <div className="kv"><span>净敞口</span>
            <b className={`num ${(detail?.exposure.net_exposure ?? 0) < 0.0001 ? 'pos' : 'neg'}`}>
              {fmt(detail?.exposure.net_exposure ?? 0, 6)}
            </b>
          </div>
        </PixelCard>
      </div>

      {/* ===== 页签：成交记录 / 资金费率流水 / 收益曲线 ===== */}
      <div style={{ display: 'flex', gap: 8, margin: '16px 0 12px' }}>
        <PixelButton variant={tab === 'fills' ? 'active' : ''} onClick={() => setTab('fills')}>成交记录</PixelButton>
        <PixelButton variant={tab === 'funding' ? 'active' : ''} onClick={() => setTab('funding')}>资金费率流水</PixelButton>
        <PixelButton variant={tab === 'profit' ? 'active' : ''} onClick={() => setTab('profit')}>收益曲线</PixelButton>
      </div>

      {tab === 'fills' && <FillsTable fills={fills} />}
      {tab === 'funding' && <FundingTable records={funding} />}
      {tab === 'profit' && <ProfitChart points={profit} />}

      {/* 局部样式：键值对行 */}
      <style>{`.kv{display:flex;justify-content:space-between;padding:6px 0;border-bottom:2px dashed #dee2e6;font-size:12px}.kv:last-child{border-bottom:none}`}</style>
    </>
  )
}

// ---------- 单腿详情卡片 ----------
function LegCard({ title, leg, isSwap }: { title: string; leg?: Detail['swap_leg']; isSwap: boolean }) {
  const rows: [string, string, string?][] = isSwap
    ? [
        ['持仓数量', fmt(leg?.amount ?? 0, 4)],
        ['均价', fmt(leg?.avg_price ?? 0, 8)],
        ['标记价', fmt(leg?.mark_price ?? 0, 8)],
        ['未实现盈亏', fmt(leg?.unrealized_pnl ?? 0, 4), (leg?.unrealized_pnl ?? 0) >= 0 ? 'pos' : 'neg'],
        ['下次费率', `${fmt(leg?.next_funding_pct ?? 0, 4)}%`],
        ['下次结算', leg?.next_settle_at || '-'],
        ['最后同步', leg?.last_sync_at || '-'],
      ]
    : [
        ['持仓数量', fmt(leg?.amount ?? 0, 4)],
        ['成本均价', fmt(leg?.avg_price ?? 0, 8)],
        ['最新价', fmt(leg?.mark_price ?? 0, 8)],
        ['持仓价值', `${fmt(leg?.value_usdt ?? 0, 2)} U`],
        ['未实现盈亏', fmt(leg?.unrealized_pnl ?? 0, 4), (leg?.unrealized_pnl ?? 0) >= 0 ? 'pos' : 'neg'],
        ['最后同步', leg?.last_sync_at || '-'],
      ]
  return (
    <PixelCard title={title}>
      {rows.map(([k, v, cls]) => (
        <div className="kv" key={k}>
          <span>{k}</span>
          <b className={cls || ''}>{v}</b>
        </div>
      ))}
    </PixelCard>
  )
}

// ---------- 成交记录页签 ----------
function FillsTable({ fills }: { fills: PositionFill[] }) {
  return (
    <PixelCard title={`成交记录（${fills.length} 条）`}>
      <PixelTable>
        <thead>
          <tr><th>交易所</th><th>腿类型</th><th>方向</th><th>价格</th><th>数量</th><th>手续费</th><th>方式</th><th>成交时间</th></tr>
        </thead>
        <tbody>
          {fills.map((f) => (
            <tr key={f.id}>
              <td><span className="pbadge blue pixel-clip-sm">{f.exchange}</span></td>
              <td>{f.market_type === 'swap' ? '合约' : '现货'}</td>
              <td className={f.side === 'buy' ? 'pos' : 'neg'}>{f.side === 'buy' ? '买入' : '卖出'}</td>
              <td className="num">{fmt(f.price, 8)}</td>
              <td className="num">{fmt(f.amount, 6)}</td>
              <td>{fmt(f.fee, 6)} {f.fee_currency}</td>
              <td><PixelBadge text={f.maker ? 'Maker' : 'Taker'} color={f.maker ? 'blue' : 'yellow'} /></td>
              <td className="muted">{fmtTime(f.traded_at)}</td>
            </tr>
          ))}
          {fills.length === 0 && <tr><td colSpan={8} className="muted">暂无成交记录</td></tr>}
        </tbody>
      </PixelTable>
    </PixelCard>
  )
}

// ---------- 资金费率流水页签 ----------
function FundingTable({ records }: { records: FundingPaymentRecord[] }) {
  return (
    <PixelCard title={`资金费率流水（${records.length} 条）`}>
      <PixelTable>
        <thead>
          <tr><th>交易所</th><th>交易对</th><th>收入 / 支出（USDT）</th><th>结算时间</th></tr>
        </thead>
        <tbody>
          {records.map((r) => (
            <tr key={r.id}>
              <td><span className="pbadge blue pixel-clip-sm">{r.exchange}</span></td>
              <td>{r.symbol}</td>
              <td className={r.amount >= 0 ? 'pos' : 'neg'}>{r.amount >= 0 ? '+' : ''}{fmt(r.amount, 6)}</td>
              <td className="muted">{fmtTime(r.income_at)}</td>
            </tr>
          ))}
          {records.length === 0 && <tr><td colSpan={4} className="muted">暂无流水（持仓经历过资金费结算后才有记录）</td></tr>}
        </tbody>
      </PixelTable>
    </PixelCard>
  )
}

// ---------- 收益曲线页签（手写 SVG 折线，无第三方图表库） ----------
function ProfitChart({ points }: { points: ProfitPoint[] }) {
  if (points.length < 2) {
    return (
      <PixelCard title="收益曲线">
        <p className="muted">数据积累中 —— 自动交易每轮会记录一次收益快照，跑一段时间后这里会出现曲线。</p>
      </PixelCard>
    )
  }
  const W = 1000, H = 320, PAD = 40
  const series = [
    { key: 'net_profit', name: '净收益', color: '#209cee' },
    { key: 'basis_pnl', name: '期现收益', color: '#92cc41' },
    { key: 'funding_cum', name: '费率累计', color: '#e59400' },
    { key: 'fee_cum', name: '手续费累计', color: '#e76e55' },
  ] as const
  // 统一的值域（所有线共用，便于对比）
  const allValues = points.flatMap((p) => series.map((s) => p[s.key] as number))
  let min = Math.min(...allValues, 0), max = Math.max(...allValues, 0)
  if (max - min < 1e-9) { max += 1; min -= 1 }
  const x = (i: number) => PAD + (i / (points.length - 1)) * (W - PAD * 2)
  const y = (v: number) => H - PAD - ((v - min) / (max - min)) * (H - PAD * 2)

  return (
    <PixelCard title={`收益曲线（${points.length} 个快照）`}>
      <div style={{ display: 'flex', gap: 14, marginBottom: 8, flexWrap: 'wrap' }}>
        {series.map((s) => (
          <span key={s.key} style={{ fontSize: 12 }}>
            <span style={{ display: 'inline-block', width: 12, height: 12, background: s.color, marginRight: 4, verticalAlign: '-2px', boxShadow: '2px 2px 0 #212529' }} />
            {s.name}
          </span>
        ))}
      </div>
      <div style={{ overflowX: 'auto' }}>
        <svg viewBox={`0 0 ${W} ${H}`} style={{ width: '100%', minWidth: 600, background: '#fffdf2', display: 'block' }}>
          {/* 网格线 */}
          {[0, 0.25, 0.5, 0.75, 1].map((t) => (
            <g key={t}>
              <line x1={PAD} x2={W - PAD} y1={PAD + t * (H - PAD * 2)} y2={PAD + t * (H - PAD * 2)} stroke="#dee2e6" strokeWidth="1" strokeDasharray="4 4" />
              <text x={4} y={PAD + t * (H - PAD * 2) + 4} fontSize="10" fill="#6c757d">
                {(max - t * (max - min)).toFixed(2)}
              </text>
            </g>
          ))}
          {/* 零轴 */}
          <line x1={PAD} x2={W - PAD} y1={y(0)} y2={y(0)} stroke="#212529" strokeWidth="2" />
          {/* 四条折线 */}
          {series.map((s) => (
            <polyline
              key={s.key}
              fill="none"
              stroke={s.color}
              strokeWidth="2.5"
              points={points.map((p, i) => `${x(i)},${y(p[s.key] as number)}`).join(' ')}
            />
          ))}
          {/* 首尾时间标注 */}
          <text x={PAD} y={H - 8} fontSize="10" fill="#6c757d">{fmtTime(points[0].time)}</text>
          <text x={W - PAD} y={H - 8} fontSize="10" fill="#6c757d" textAnchor="end">{fmtTime(points[points.length - 1].time)}</text>
        </svg>
      </div>
    </PixelCard>
  )
}
