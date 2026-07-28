// 【阅读顺序 25h】日志页 —— 交易 / 自动交易 / 预警 统一操作日志（像素终端风格）。
import { useEffect, useState } from 'react'
import { api, TradeLog, fmtTime } from '../api'
import { PixelButton, PixelCard, PixelTable } from '../components/Pixel'

export default function LogsPage() {
  const [list, setList] = useState<TradeLog[]>([])

  const load = () => api.tradeLogs().then(setList).catch(() => {})
  useEffect(() => { load() }, [])

  const levelClass = (lv: string) => (lv === 'error' ? 'neg' : lv === 'warn' ? 'warn' : 'pos')

  return (
    <PixelCard title="操作日志（最近 200 条）">
      <div style={{ marginBottom: 12 }}>
        <PixelButton onClick={load}>🔄 刷新</PixelButton>
      </div>
      <div className="term">
        <PixelTable>
          <thead>
            <tr><th>时间</th><th>模块</th><th>级别</th><th>动作</th><th>币对</th><th>内容</th></tr>
          </thead>
          <tbody>
            {list.map((l) => (
              <tr key={l.id}>
                <td className="muted">{fmtTime(l.time)}</td>
                <td><span className="pbadge blue pixel-clip-sm">{l.module}</span></td>
                <td className={levelClass(l.level)}>{l.level}</td>
                <td>{l.action}</td>
                <td>{l.symbol || '-'}</td>
                <td style={{ whiteSpace: 'normal', minWidth: 300 }}>{l.message}</td>
              </tr>
            ))}
            {list.length === 0 && <tr><td colSpan={6} className="muted">暂无日志</td></tr>}
          </tbody>
        </PixelTable>
      </div>
    </PixelCard>
  )
}
