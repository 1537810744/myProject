// 模块 4：账户页 —— 双所资金、聚合总览、合约实时持仓
import { useEffect, useState } from 'react'
import { api, AccountOverview, fmt } from '../api'
import { PixelButton, PixelCard, PixelTable, StatCard } from '../components/Pixel'

export default function AccountPage() {
  const [ov, setOv] = useState<AccountOverview | null>(null)

  const load = () => api.overview().then(setOv).catch(() => {})
  useEffect(() => { load() }, [])

  // 运行时长（人性化显示）
  const runDuration = (): string => {
    if (!ov?.running_since) return '-'
    const secs = Math.floor((Date.now() - new Date(ov.running_since).getTime()) / 1000)
    if (secs < 0) return '-'
    const h = Math.floor(secs / 3600)
    const m = Math.floor((secs % 3600) / 60)
    return h > 0 ? `${h}h ${m}m` : `${m}m ${secs % 60}s`
  }

  return (
    <>
      <div style={{ marginBottom: 12 }}>
        <PixelButton variant="primary" onClick={load}>🔄 刷新</PixelButton>
      </div>
      <div className="stat-grid">
        {(ov?.balances || []).map((b) => (
          <StatCard
            key={b.exchange + b.market_type}
            label={`${b.exchange}（${b.market_type === 'spot' ? '现货' : '合约'}）USDT`}
            value={fmt(b.usdt_total, 2)}
            sub={`可用 ${fmt(b.usdt_free, 2)} / 冻结 ${fmt(b.usdt_used, 2)}`}
          />
        ))}
        <StatCard label="聚合总资金" value={`${fmt(ov?.total_usdt ?? 0, 2)} U`} color="green" />
        <StatCard label="购买力 min(合约×杠杆, 现货)" value={`${fmt(ov?.purchasing_power ?? 0, 2)} U`} />
        <StatCard label="持有对冲对" value={String(ov?.hedges?.length ?? 0)} />
        <StatCard label="运行时长" value={runDuration()} />
      </div>

      <PixelCard title="合约实时持仓">
        <PixelTable>
          <thead>
            <tr><th>交易所</th><th>币对</th><th>方向</th><th>数量</th><th>开仓价</th><th>标记价</th><th>未实现盈亏</th><th>杠杆</th><th>强平价</th></tr>
          </thead>
          <tbody>
            {(ov?.swap_positions || []).map((p) => (
              <tr key={p.exchange + p.symbol}>
                <td><span className="pbadge blue pixel-clip-sm">{p.exchange}</span></td>
                <td><b>{p.symbol}</b></td>
                <td className={p.side === 'short' ? 'neg' : 'pos'}>{p.side === 'short' ? '空' : '多'}</td>
                <td className="num">{fmt(p.contracts, 4)}</td>
                <td className="num">{fmt(p.entry_price, 6)}</td>
                <td className="num">{fmt(p.mark_price, 6)}</td>
                <td className={p.unrealized_pnl >= 0 ? 'pos' : 'neg'}>{fmt(p.unrealized_pnl, 2)}</td>
                <td>{fmt(p.leverage, 0)}x</td>
                <td className="warn">{fmt(p.liquidation_price, 6)}</td>
              </tr>
            ))}
            {(ov?.swap_positions || []).length === 0 && <tr><td colSpan={9} className="muted">无合约持仓</td></tr>}
          </tbody>
        </PixelTable>
      </PixelCard>
    </>
  )
}
