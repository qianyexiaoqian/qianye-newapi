/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { createContext, useContext, useEffect } from 'react'

import {
  DEFAULT_THEME_CUSTOMIZATION,
  FIXED_THEME_PRESET,
  THEME_PRESET_ATTRIBUTE,
  type ThemeCustomization,
} from '@/lib/theme-customization'

/**
 * 主题定制的读取入口。
 *
 * ── 千夜:这里已经没有任何可调的东西了 ──
 * 项目方裁决「移除主题设置功能」,五个可调轴(preset / font / radius / scale /
 * contentLayout)连同它们的 cookie 读写、setter、`data-theme-*` 属性写入一并
 * 删除。留下来的只有一件事:**把固定预设写到 `<body>` 上**。
 *
 * 那一个属性不能省:整套 Steins Gate 主题 CSS 都挂在
 * `[data-theme-preset='steins-gate']` 作用域下,不写等于主题整个失效。
 *
 * Provider 本身保留(而不是把 `<ThemeCustomizationProvider>` 从 `__root.tsx`
 * 摘掉),因为上游三张图表仍在调 {@link useThemeCustomization} 取刷新键;
 * 换成直接 import 常量要动三个上游文件,合并上游时白白多三处冲突。
 */
const CONTEXT_VALUE: { customization: ThemeCustomization } = {
  customization: DEFAULT_THEME_CUSTOMIZATION,
}

const ThemeCustomizationContext = createContext(CONTEXT_VALUE)

export function ThemeCustomizationProvider(props: {
  children: React.ReactNode
}) {
  // 属性写在 <body> 而不是 <html>:上游 theme-presets.css 与 qy-sg-*.css 的
  // 选择器口径都是 body,换一个挂载点会让全部预设选择器一起失配。
  useEffect(() => {
    document.body?.setAttribute(THEME_PRESET_ATTRIBUTE, FIXED_THEME_PRESET)
  }, [])

  return (
    <ThemeCustomizationContext.Provider value={CONTEXT_VALUE}>
      {props.children}
    </ThemeCustomizationContext.Provider>
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export function useThemeCustomization() {
  return useContext(ThemeCustomizationContext)
}
