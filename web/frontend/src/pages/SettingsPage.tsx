// 模块 7：设置页 —— 全部参数公开显示，保存即生效
import { useEffect, useState } from 'react'
import { api, SettingItem } from '../api'
import { PixelButton, PixelCard, PixelMessage, useMessage } from '../components/Pixel'

export default function SettingsPage() {
  const [items, setItems] = useState<SettingItem[]>([])
  const [values, setValues] = useState<Record<string, string>>({})
  const [msg, showMsg] = useMessage()
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api.settings().then((list) => {
      setItems(list)
      const v: Record<string, string> = {}
      list.forEach((p) => { v[p.key] = p.value })
      setValues(v)
    }).catch(() => {})
  }, [])

  const save = async () => {
    setSaving(true)
    try {
      await api.saveSettings(values)
      showMsg('ok', '✔ 保存成功，即时生效（自动交易下一轮起使用新参数）')
    } catch (e) {
      showMsg('err', '✘ 保存失败：' + (e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  // 按用途分组渲染，页面更清晰
  const groups: { title: string; keys: string[] }[] = [
    { title: '行情过滤', keys: ['funding_count', 'min_basis_pct', 'min_funding_avg_pct', 'min_quote_volume_24h'] },
    { title: '卖出策略', keys: ['hold_sell_threshold_pct'] },
    { title: '交易执行', keys: ['group_size_usdt', 'atom_size_usdt', 'dust_usdt', 'max_buy_pairs'] },
    { title: '自动交易', keys: ['loop_interval_sec', 'auto_trade_enabled'] },
    { title: '资金与杠杆', keys: ['leverage', 'balance_ratio', 'balance_warn_pct'] },
    { title: '邮件通知', keys: ['smtp_host', 'smtp_port', 'smtp_user', 'smtp_pass', 'mail_from', 'mail_to'] },
  ]
  const byKey = (k: string) => items.find((i) => i.key === k)

  return (
    <>
      {groups.map((g) => (
        <PixelCard key={g.title} title={g.title}>
          <div className="form-grid">
            {g.keys.map((k) => {
              const item = byKey(k)
              if (!item) return null
              const isSecret = k === 'smtp_pass'
              return (
                <div className="pfield" key={k}>
                  <label>{item.key}</label>
                  <div className="pinput pixel-clip-sm">
                    <input
                      className="pixel-clip-sm"
                      type={isSecret ? 'password' : 'text'}
                      value={values[k] ?? ''}
                      placeholder={`默认 ${item.default || '(空)'}`}
                      onChange={(e) => setValues({ ...values, [k]: e.target.value })}
                    />
                  </div>
                  <div className="hint">{item.description}</div>
                </div>
              )
            })}
          </div>
        </PixelCard>
      ))}
      <div style={{ marginTop: 16 }}>
        <PixelButton variant="success" onClick={save} disabled={saving}>
          {saving ? '保存中...' : '💾 保存全部'}
        </PixelButton>
      </div>
      <PixelMessage kind={msg.kind} text={msg.text} />
    </>
  )
}
