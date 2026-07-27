// 应用主框架：像素标题牌 + 页签导航 + 页面容器
import { useState } from 'react'
import { PixelButton, SkyDecor } from './components/Pixel'
import ApisPage from './pages/ApisPage'
import MarketPage from './pages/MarketPage'
import TradePage from './pages/TradePage'
import AccountPage from './pages/AccountPage'
import AlertPage from './pages/AlertPage'
import AutoPage from './pages/AutoPage'
import SettingsPage from './pages/SettingsPage'
import LogsPage from './pages/LogsPage'

// 页签定义（key -> 中文名）
const PAGES = [
  { key: 'apis', name: '🔑 API配置' },
  { key: 'market', name: '📈 行情' },
  { key: 'trade', name: '⚔ 交易' },
  { key: 'account', name: '💰 账户' },
  { key: 'alert', name: '⚠ 预警' },
  { key: 'auto', name: '🤖 自动交易' },
  { key: 'settings', name: '⚙ 设置' },
  { key: 'logs', name: '📜 日志' },
] as const

type PageKey = (typeof PAGES)[number]['key']

export default function App() {
  const [page, setPage] = useState<PageKey>('apis')

  return (
    <>
      {/* 天空背景：像素云漂移 + 星星闪烁 */}
      <SkyDecor />

      <div className="app">
        {/* 标题牌 */}
        <div className="title-banner">
          <h1>★ DELTA CRYPTO ★</h1>
          <br />
          <span className="sub pixel-clip-sm">资金费率 + 基差套利 · 像素小屋</span>
        </div>

        {/* 像素页签导航 */}
        <nav className="nav">
          {PAGES.map((p) => (
            <PixelButton
              key={p.key}
              variant={page === p.key ? 'active' : ''}
              onClick={() => setPage(p.key)}
            >
              {p.name}
            </PixelButton>
          ))}
        </nav>

        {/* 页面内容 */}
        <main>
          {page === 'apis' && <ApisPage />}
          {page === 'market' && <MarketPage />}
          {page === 'trade' && <TradePage />}
          {page === 'account' && <AccountPage />}
          {page === 'alert' && <AlertPage />}
          {page === 'auto' && <AutoPage />}
          {page === 'settings' && <SettingsPage />}
          {page === 'logs' && <LogsPage />}
        </main>

        <div className="footer">PRESS START · 单进程 + Goroutine · 本机运行</div>
      </div>
    </>
  )
}
