// 模块 5：预警页 —— 预警记录 + 手动触发检查
import { useEffect, useState } from 'react'
import { api, AlertRecord, fmtTime } from '../api'
import { PixelButton, PixelCard, PixelTable, PixelMessage, useMessage } from '../components/Pixel'

// 预警类型的中文名与徽章色
const typeMeta: Record<string, { text: string; color: string }> = {
  funding_negative: { text: '费率反转', color: 'red' },
  adl: { text: 'ADL 风险', color: 'red' },
  liquidation: { text: '爆仓风险', color: 'red' },
  balance: { text: '资金平衡', color: 'yellow' },
}

export default function AlertPage() {
  const [list, setList] = useState<AlertRecord[]>([])
  const [msg, showMsg] = useMessage()
  const [checking, setChecking] = useState(false)

  const load = () => api.alertRecords().then(setList).catch(() => {})
  useEffect(() => { load() }, [])

  const check = async () => {
    setChecking(true)
    try {
      const fired = await api.alertCheck()
      showMsg('ok', fired && fired.length ? `⚠ 本轮触发 ${fired.length} 条预警` : '✔ 本轮无预警，一切正常')
      load()
    } catch (e) {
      showMsg('err', '✘ 检查失败：' + (e as Error).message)
    } finally {
      setChecking(false)
    }
  }

  return (
    <PixelCard title="预警记录（费率反转 / ADL / 爆仓 / 资金平衡，触发自动发邮件）">
      <div style={{ display: 'flex', gap: 10, marginBottom: 12 }}>
        <PixelButton variant="warning" onClick={check} disabled={checking}>
          {checking ? '检查中...' : '🔍 立即检查一轮'}
        </PixelButton>
        <PixelButton onClick={load}>🔄 刷新记录</PixelButton>
      </div>
      <PixelMessage kind={msg.kind} text={msg.text} />
      <PixelTable>
        <thead>
          <tr><th>时间</th><th>类型</th><th>币对</th><th>级别</th><th>内容</th><th>邮件</th></tr>
        </thead>
        <tbody>
          {list.map((r) => {
            const meta = typeMeta[r.type] || { text: r.type, color: '' }
            return (
              <tr key={r.id}>
                <td className="muted">{fmtTime(r.time)}</td>
                <td><span className={`pbadge pixel-clip-sm ${meta.color}`}>{meta.text}</span></td>
                <td>{r.symbol || '-'}</td>
                <td className={r.level === 'critical' ? 'neg' : 'warn'}>{r.level}</td>
                <td style={{ whiteSpace: 'normal', minWidth: 260 }}>{r.message}</td>
                <td>{r.mail_sent ? '✔' : '✘'}</td>
              </tr>
            )
          })}
          {list.length === 0 && <tr><td colSpan={6} className="muted">暂无预警记录</td></tr>}
        </tbody>
      </PixelTable>
    </PixelCard>
  )
}
