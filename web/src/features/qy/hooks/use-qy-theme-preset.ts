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
import { useSyncExternalStore } from 'react'

import { THEME_PRESET_ATTRIBUTE } from '@/lib/theme-customization'

/**
 * 当前生效的主题预设（读 `<body>` 的 `data-theme-preset`）。
 *
 * **刻意读 DOM 属性而不是 `useThemeCustomization().customization.preset`。**
 * CSS 认的是属性，React 侧的条件渲染必须认同一个东西，否则会出现
 * "配色变了但区段头没出现"的割裂。属性名从 `lib/theme-customization` 取，
 * 与写入方共用同一个常量。
 *
 * 属性由 `ThemeCustomizationProvider` 的 effect 写入，首帧（SSR/水合前）为空，
 * 因此 `getServerSnapshot` 返回 `null` —— 退化成普通标题，属于安全方向。
 * 主题设置移除后预设已固定为 steins-gate，这里的返回值实际上只会从
 * `false` 翻到 `true` 一次，不会再翻回去。
 */
function subscribe(onChange: () => void): () => void {
  if (typeof MutationObserver === 'undefined') return () => {}
  const observer = new MutationObserver(onChange)
  observer.observe(document.body, {
    attributes: true,
    attributeFilter: [THEME_PRESET_ATTRIBUTE],
  })
  return () => observer.disconnect()
}

function readPreset(): string | null {
  if (typeof document === 'undefined') return null
  return document.body?.getAttribute(THEME_PRESET_ATTRIBUTE) ?? null
}

/** Steins Gate 主题的专属构图（区段头、编号行、概览栏）只在该预设下渲染。 */
export function useQyIsSteinsGate(): boolean {
  return (
    useSyncExternalStore(subscribe, readPreset, () => null) === 'steins-gate'
  )
}
