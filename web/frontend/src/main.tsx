// 【阅读顺序 20】前端入口 —— 前端读代码从这里开始。
// 本文件职责：引入像素字体与全局样式，把 <App/> 挂载到页面根节点。
// 阅读路径建议：21 样式系统 → 22 API 层 → 23 像素组件 → 24 App 导航 → 25 各页面。
import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'

// 像素字体（fontsource 本地打包，无需外网）：
// - Press Start 2P：英文/数字像素字体（标题、数值）
// - Fusion Pixel 12px SC：中文像素字体（正文）
import '@fontsource/press-start-2p'
import '@fontsource/fusion-pixel-12px-proportional-sc'

import './styles/pixel.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
