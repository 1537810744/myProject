// 前端入口：挂载 React 根组件
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
