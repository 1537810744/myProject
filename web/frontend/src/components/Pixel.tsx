// 【阅读顺序 23】像素风通用组件库。
// 本文件职责：把 pixel.css 的样式机关封装成 React 组件（按钮/卡片/输入框/表格/
// 徽章/统计卡/弹窗/天空装饰），页面文件里全是这些积木的组合。
// 阅读目的：重点看 PixelButton 和 PixelCard 两个，其余都是同一套路的变体。
import React, { useState } from 'react'

// ---------- 像素按钮 ----------
// variant: 默认白 / primary 蓝 / success 绿 / warning 黄 / error 红 / active 选中态
export function PixelButton({
  children,
  variant = '',
  onClick,
  disabled,
  title,
}: {
  children: React.ReactNode
  variant?: '' | 'primary' | 'success' | 'warning' | 'error' | 'active'
  onClick?: () => void
  disabled?: boolean
  title?: string
}) {
  return (
    <button
      className={`pbtn pixel-clip-sm ${variant}`}
      onClick={onClick}
      disabled={disabled}
      title={title}
    >
      <span className="pixel-clip-sm">{children}</span>
    </button>
  )
}

// ---------- 像素卡片 ----------
export function PixelCard({
  title,
  children,
}: {
  title?: string
  children: React.ReactNode
}) {
  return (
    <div className="pf pixel-clip pcard">
      <div className="pf-in pixel-clip">
        {title && <div className="pcard-title">{title}</div>}
        {children}
      </div>
    </div>
  )
}

// ---------- 像素输入框 ----------
export function PixelInput({
  label,
  value,
  onChange,
  placeholder,
  type = 'text',
  hint,
}: {
  label?: string
  value: string
  onChange: (v: string) => void
  placeholder?: string
  type?: string
  hint?: string
}) {
  return (
    <div className="pfield">
      {label && <label>{label}</label>}
      <div className="pinput pixel-clip-sm">
        <input
          className="pixel-clip-sm"
          type={type}
          value={value}
          placeholder={placeholder}
          onChange={(e) => onChange(e.target.value)}
        />
      </div>
      {hint && <div className="hint">{hint}</div>}
    </div>
  )
}

// ---------- 像素下拉框 ----------
export function PixelSelect({
  label,
  value,
  onChange,
  options,
}: {
  label?: string
  value: string
  onChange: (v: string) => void
  options: { value: string; text: string }[]
}) {
  return (
    <div className="pfield">
      {label && <label>{label}</label>}
      <div className="pinput pixel-clip-sm">
        <select className="pixel-clip-sm" value={value} onChange={(e) => onChange(e.target.value)}>
          {options.map((o) => (
            <option key={o.value} value={o.value}>
              {o.text}
            </option>
          ))}
        </select>
      </div>
    </div>
  )
}

// ---------- 消息条 ----------
// kind: ok 绿底 / err 红底；text 为空时不显示
export function PixelMessage({ kind, text }: { kind: 'ok' | 'err'; text: string }) {
  if (!text) return null
  return (
    <div className={`pf pixel-clip-sm pmsg show ${kind}`}>
      <div className="pf-in pixel-clip-sm">{text}</div>
    </div>
  )
}

// ---------- 徽章 ----------
export function PixelBadge({
  text,
  color = '',
}: {
  text: string
  color?: '' | 'on' | 'off' | 'blue' | 'yellow' | 'red'
}) {
  return <span className={`pbadge pixel-clip-sm ${color}`}>{text}</span>
}

// ---------- 统计卡片 ----------
export function StatCard({
  label,
  value,
  sub,
  color = '',
}: {
  label: string
  value: string
  sub?: string
  color?: '' | 'green' | 'red'
}) {
  return (
    <div className="pf pixel-clip">
      <div className="pf-in pixel-clip" style={{ padding: '12px' }}>
        <div className="stat-label">{label}</div>
        <div className={`stat-num ${color}`}>{value}</div>
        {sub && <div className="hint">{sub}</div>}
      </div>
    </div>
  )
}

// ---------- 表格容器（像素边框 + 横向滚动） ----------
export function PixelTable({ children }: { children: React.ReactNode }) {
  return (
    <div className="ptable-wrap pixel-clip-sm">
      <table className="ptable pixel-clip-sm">{children}</table>
    </div>
  )
}

// ---------- 确认弹窗（像素风，替代浏览器 confirm） ----------
export function PixelConfirm({
  open,
  text,
  onOk,
  onCancel,
}: {
  open: boolean
  text: string
  onOk: () => void
  onCancel: () => void
}) {
  if (!open) return null
  return (
    <div
      style={{
        position: 'fixed', inset: 0, zIndex: 99,
        background: 'rgba(33,37,41,0.5)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
      }}
      onClick={onCancel}
    >
      <div className="pf pixel-clip" style={{ minWidth: 300 }} onClick={(e) => e.stopPropagation()}>
        <div className="pf-in pixel-clip" style={{ padding: 20, textAlign: 'center' }}>
          <p style={{ marginBottom: 16 }}>{text}</p>
          <div style={{ display: 'flex', gap: 12, justifyContent: 'center' }}>
            <PixelButton variant="error" onClick={onOk}>确认</PixelButton>
            <PixelButton onClick={onCancel}>取消</PixelButton>
          </div>
        </div>
      </div>
    </div>
  )
}

// ---------- 天空装饰：像素云 + 闪烁星星（纯 CSS 绘制，fixed 背景层） ----------
export function SkyDecor() {
  // 手工摆放的云与星星（位置/动画时长各不相同，营造游戏天空感）
  const clouds = [
    { top: '6%', delay: '0s', dur: '90s', scale: 1.4 },
    { top: '18%', delay: '-30s', dur: '110s', scale: 0.9 },
    { top: '70%', delay: '-60s', dur: '130s', scale: 1.1 },
    { top: '45%', delay: '-15s', dur: '100s', scale: 0.7 },
  ]
  const stars = [
    { top: '10%', left: '12%' }, { top: '22%', left: '78%' },
    { top: '8%', left: '55%' }, { top: '32%', left: '30%' },
    { top: '60%', left: '88%' }, { top: '80%', left: '8%' },
    { top: '38%', left: '62%' }, { top: '88%', left: '48%' },
  ]
  return (
    <div className="sky-decor">
      {clouds.map((c, i) => (
        <div
          key={`c${i}`}
          className="pixel-cloud cloud-drift"
          style={{
            top: c.top,
            animationDelay: c.delay,
            animationDuration: c.dur,
            scale: String(c.scale),
          }}
        />
      ))}
      {stars.map((s, i) => (
        <div key={`s${i}`} className="pixel-star" style={{ top: s.top, left: s.left, animationDelay: `${i * 0.3}s` }} />
      ))}
    </div>
  )
}

// ----------  loading 状态的小工具 ----------
export function useMessage(): [{ kind: 'ok' | 'err'; text: string }, (kind: 'ok' | 'err', text: string) => void] {
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string }>({ kind: 'ok', text: '' })
  return [msg, (kind, text) => setMsg({ kind, text })]
}
