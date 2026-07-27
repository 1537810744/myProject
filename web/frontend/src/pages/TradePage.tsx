// 模块 3：交易页 —— 手动建仓/平仓 + 当前对冲持仓
import { useEffect, useState } from 'react'
import { api, HedgePosition, fmt } from '../api'
import { PixelButton, PixelCard, PixelInput, PixelMessage, PixelTable, PixelConfirm, useMessage } from '../components/Pixel'

export default function TradePage() {
  const [symbol, setSymbol] = useState('')
  const [total, setTotal] = useState('')
  const [atom, setAtom] = useState('')
  const [positions, setPositions] = useState<HedgePosition[]>([])
  const [msg, showMsg] = useMessage()
  const [busy, setBusy] = useState(false)
  const [closing, setClosing] = useState<HedgePosition | null>(null)

  const loadPositions = () =>
    api.overview().then((ov) => setPositions(ov.hedges || [])).catch(() => {})
  useEffect(() => { loadPositions() }, [])

  const open = async () => {
    if (!symbol.trim()) { showMsg('err', '✘ 请填写币对'); return }
    setBusy(true)
    try {
      const r = await api.tradeOpen({
        symbol: symbol.trim(),
        total_usdt: parseFloat(total) || 0,
        atom_usdt: parseFloat(atom) || 0,
      })
      showMsg('ok', '✔ ' + r.message)
      loadPositions()
    } catch (e) {
      showMsg('err', '✘ 建仓失败：' + (e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const close = async () => {
    if (!closing) return
    setBusy(true)
    try {
      const r = await api.tradeClose({ symbol: closing.symbol, position_id: closing.id })
      showMsg('ok', '✔ ' + r.message)
    } catch (e) {
      showMsg('err', '✘ 平仓失败：' + (e as Error).message)
    } finally {
      setBusy(false)
      setClosing(null)
      loadPositions()
    }
  }

  return (
    <>
      <PixelCard title="手动对冲交易（建仓 = 买现货 + 空合约；双腿市价单）">
        <div className="form-grid">
          <PixelInput label="币对（如 BTC/USDT）" value={symbol} onChange={setSymbol} placeholder="BTC/USDT" />
          <PixelInput label="总量 USDT（留空读设置）" value={total} onChange={setTotal} placeholder="50" type="number" />
          <PixelInput label="原子单位 USDT（留空读设置）" value={atom} onChange={setAtom} placeholder="5" type="number" />
        </div>
        <div style={{ display: 'flex', gap: 10, marginTop: 4 }}>
          <PixelButton variant="success" onClick={open} disabled={busy}>{busy ? '执行中...' : '▲ 建仓'}</PixelButton>
        </div>
        <PixelMessage kind={msg.kind} text={msg.text} />
      </PixelCard>

      <PixelCard title={`当前对冲持仓（${positions.length}）`}>
        <PixelTable>
          <thead>
            <tr><th>ID</th><th>币对</th><th>现货量</th><th>合约量</th><th>现货均价</th><th>合约均价</th><th>入场基差%</th><th>建仓时间</th><th>操作</th></tr>
          </thead>
          <tbody>
            {positions.map((p) => (
              <tr key={p.id}>
                <td>{p.id}</td>
                <td><b>{p.symbol}</b></td>
                <td className="num">{fmt(p.spot_amount, 6)}</td>
                <td className="num">{fmt(p.swap_amount, 6)}</td>
                <td className="num">{fmt(p.spot_entry_price, 6)}</td>
                <td className="num">{fmt(p.swap_entry_price, 6)}</td>
                <td className="pos">{fmt(p.entry_basis_pct)}%</td>
                <td className="muted">{p.opened_at}</td>
                <td><PixelButton variant="error" onClick={() => setClosing(p)}>平仓</PixelButton></td>
              </tr>
            ))}
            {positions.length === 0 && <tr><td colSpan={9} className="muted">暂无持仓</td></tr>}
          </tbody>
        </PixelTable>
      </PixelCard>

      <PixelConfirm
        open={!!closing}
        text={`确认平仓 ${closing?.symbol} 全部持仓？（卖现货 + 平空合约）`}
        onOk={close}
        onCancel={() => setClosing(null)}
      />
    </>
  )
}
