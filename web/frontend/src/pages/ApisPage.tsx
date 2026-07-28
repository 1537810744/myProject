// 【阅读顺序 25a】模块 1：API 配置页 —— 保存凭证、测试连通性/权限、列表管理。
import { useEffect, useState } from 'react'
import { api, ExchangeAPI } from '../api'
import { PixelButton, PixelCard, PixelInput, PixelSelect, PixelMessage, PixelTable, PixelConfirm, useMessage } from '../components/Pixel'

export default function ApisPage() {
  const [exchange, setExchange] = useState('gate')
  const [label, setLabel] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [apiSecret, setApiSecret] = useState('')
  const [list, setList] = useState<ExchangeAPI[]>([])
  const [msg, showMsg] = useMessage()
  const [testing, setTesting] = useState(false)
  const [confirmDel, setConfirmDel] = useState('')

  const load = () => api.listAPIs().then(setList).catch(() => {})
  useEffect(() => { load() }, [])

  const save = async () => {
    try {
      await api.saveAPI({ exchange, label, api_key: apiKey, api_secret: apiSecret })
      showMsg('ok', '✔ 保存成功，交易所连接已热更新')
      setApiKey(''); setApiSecret('')
      load()
    } catch (e) {
      showMsg('err', '✘ 保存失败：' + (e as Error).message)
    }
  }

  const test = async () => {
    setTesting(true)
    showMsg('ok', '⏳ 测试中（需加载市场信息，约几秒）...')
    try {
      const r = await api.testAPI({
        exchange,
        role: exchange === 'gate' ? 'spot' : 'swap',
        api_key: apiKey, api_secret: apiSecret,
      })
      const ok = r.connected && r.permission
      showMsg(ok ? 'ok' : 'err',
        `连通性 ${r.connected ? '✔' : '✘'}　权限 ${r.permission ? '✔' : '✘'}　—　${r.message}`)
    } catch (e) {
      showMsg('err', '✘ 测试失败：' + (e as Error).message)
    } finally {
      setTesting(false)
    }
  }

  const doDelete = async () => {
    await api.deleteAPI(confirmDel)
    setConfirmDel('')
    load()
  }

  return (
    <>
      <PixelCard title="新增 / 更新 API Key">
        <div className="form-grid">
          <PixelSelect
            label="交易所"
            value={exchange}
            onChange={setExchange}
            options={[
              { value: 'gate', text: 'gate（现货腿）' },
              { value: 'binance', text: 'binance（合约腿）' },
            ]}
          />
          <PixelInput label="标签（可选）" value={label} onChange={setLabel} placeholder="如：币安主账户" />
          <PixelInput label="API Key" value={apiKey} onChange={setApiKey} placeholder="输入 API Key" />
          <PixelInput label="API Secret" value={apiSecret} onChange={setApiSecret} placeholder="输入 API Secret" type="password" />
        </div>
        <p className="hint">★ 只需开启「现货/合约交易」权限，切勿开启提币权限；建议在交易所后台绑定本机 IP 白名单。</p>
        <div style={{ display: 'flex', gap: 10, marginTop: 12 }}>
          <PixelButton variant="success" onClick={save}>保存</PixelButton>
          <PixelButton variant="primary" onClick={test} disabled={testing}>
            {testing ? '测试中...' : '测试连接'}
          </PixelButton>
        </div>
        <PixelMessage kind={msg.kind} text={msg.text} />
      </PixelCard>

      <PixelCard title="已保存的凭证">
        <PixelTable>
          <thead>
            <tr><th>交易所</th><th>标签</th><th>API Key</th><th>Secret</th><th>保存时间</th><th>操作</th></tr>
          </thead>
          <tbody>
            {list.map((a) => (
              <tr key={a.id}>
                <td><span className="pbadge blue pixel-clip-sm">{a.exchange}</span></td>
                <td>{a.label || '-'}</td>
                <td>{a.api_key}</td>
                <td className="muted">{a.api_secret}</td>
                <td className="muted">{a.created_at}</td>
                <td><PixelButton variant="error" onClick={() => setConfirmDel(a.exchange)}>删除</PixelButton></td>
              </tr>
            ))}
            {list.length === 0 && <tr><td colSpan={6} className="muted">暂无凭证，请先添加</td></tr>}
          </tbody>
        </PixelTable>
      </PixelCard>

      <PixelConfirm
        open={!!confirmDel}
        text={`确认删除 ${confirmDel} 的凭证？`}
        onOk={doDelete}
        onCancel={() => setConfirmDel('')}
      />
    </>
  )
}
