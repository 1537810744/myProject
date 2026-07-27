// 模块 2：行情页 —— 通过全部约束的优质对冲标的（待买入列表）
import { useState } from 'react'
import { api, MarketCandidate, fmt } from '../api'
import { PixelButton, PixelCard, PixelTable, PixelMessage, useMessage } from '../components/Pixel'

export default function MarketPage() {
  const [list, setList] = useState<MarketCandidate[]>([])
  const [loading, setLoading] = useState(false)
  const [updatedAt, setUpdatedAt] = useState('')
  const [msg, showMsg] = useMessage()

  const load = async () => {
    setLoading(true)
    try {
      const data = await api.candidates()
      setList(data || [])
      setUpdatedAt(new Date().toLocaleTimeString())
    } catch (e) {
      showMsg('err', '✘ 行情加载失败：' + (e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <PixelCard title="待买入列表（基差 / 费率趋势 / 费率均值 / 流通量 四重过滤）">
      <div style={{ display: 'flex', gap: 10, alignItems: 'center', marginBottom: 12 }}>
        <PixelButton variant="primary" onClick={load} disabled={loading}>
          {loading ? '⏳ 加载中...' : '🔄 刷新行情'}
        </PixelButton>
        <span className="hint">
          {loading ? '首次加载需拉取历史费率，约几秒钟' : updatedAt ? `更新于 ${updatedAt}，共 ${list.length} 个标的` : '点击刷新获取最新行情'}
        </span>
      </div>
      <PixelMessage kind={msg.kind} text={msg.text} />
      <PixelTable>
        <thead>
          <tr>
            <th>币种</th><th>合约价</th><th>现货价</th><th>基差%</th>
            <th>当前费率%</th><th>N次均值%</th><th>年化%</th><th>24H合约量</th><th>推荐方向</th>
          </tr>
        </thead>
        <tbody>
          {list.map((c) => (
            <tr key={c.symbol}>
              <td><b>{c.symbol}</b></td>
              <td className="num">{fmt(c.swap_price, 6)}</td>
              <td className="num">{fmt(c.spot_price, 6)}</td>
              <td className={c.basis_pct >= 0 ? 'pos' : 'neg'}>{fmt(c.basis_pct)}%</td>
              <td className={c.funding_rate >= 0 ? 'pos' : 'neg'}>{fmt(c.funding_rate)}%</td>
              <td>{fmt(c.funding_avg_pct)}%</td>
              <td className="pos">{fmt(c.annualized_pct, 1)}%</td>
              <td>{(c.quote_volume_24h / 1e6).toFixed(2)}M</td>
              <td className="muted">{c.direction}</td>
            </tr>
          ))}
          {list.length === 0 && !loading && (
            <tr><td colSpan={9} className="muted">暂无通过过滤的标的（没有优质机会时这张表就是空的，正常）</td></tr>
          )}
        </tbody>
      </PixelTable>
    </PixelCard>
  )
}
