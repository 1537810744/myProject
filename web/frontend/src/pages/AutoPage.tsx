// 模块 6：自动交易页 —— 运行状态 + 手动触发一轮
import { useEffect, useState } from 'react'
import { api, AutoStatus, fmtTime } from '../api'
import { PixelButton, PixelCard, StatCard, PixelMessage, useMessage } from '../components/Pixel'

export default function AutoPage() {
  const [st, setSt] = useState<AutoStatus | null>(null)
  const [msg, showMsg] = useMessage()

  const load = () => api.autoStatus().then(setSt).catch(() => {})
  useEffect(() => { load() }, [])

  const run = async () => {
    try {
      await api.autoRun()
      showMsg('ok', '✔ 已触发一轮执行，约 8 秒后自动刷新状态...')
      setTimeout(load, 8000) // 异步执行，延迟刷新看结果
    } catch (e) {
      showMsg('err', '✘ 触发失败：' + (e as Error).message)
    }
  }

  return (
    <>
      <div className="stat-grid">
        <StatCard
          label="总开关（「设置」页 auto_trade_enabled 控制）"
          value={st?.enabled ? '▶ 运行中' : '■ 已关闭'}
          color={st?.enabled ? 'green' : 'red'}
        />
        <StatCard label="累计执行轮数" value={String(st?.round_count ?? 0)} />
        <StatCard label="最近一轮时间" value={fmtTime(st?.last_run_at || '')} />
      </div>

      <PixelCard title="控制台">
        <div style={{ display: 'flex', gap: 10 }}>
          <PixelButton variant="success" onClick={run}>⚡ 立即执行一轮</PixelButton>
          <PixelButton onClick={load}>🔄 刷新状态</PixelButton>
        </div>
        <PixelMessage kind={msg.kind} text={msg.text} />
        <p className="hint" style={{ marginTop: 10 }}>
          ★ 自动交易在后台按设置间隔循环运行（卖出阶段 → 买入阶段），本页仅展示状态与手动触发。
        </p>
      </PixelCard>

      <PixelCard title="最近一轮摘要">
        <div className="term pf pixel-clip-sm" style={{ display: 'block' }}>
          <div className="pf-in pixel-clip-sm" style={{ padding: 12 }}>
            <pre style={{ color: '#92cc41', fontFamily: 'var(--font-zh)', fontSize: 12, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
              {st?.last_summary || '暂无记录'}
            </pre>
          </div>
        </div>
      </PixelCard>
    </>
  )
}
