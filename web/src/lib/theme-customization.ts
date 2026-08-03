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
/**
 * Theme customization constants and types.
 *
 * Lives in `lib/` (not `context/`) so it can be imported alongside the
 * provider without breaking React Fast Refresh boundaries.
 *
 * ── 千夜:主题定制已被收成一个常量 ──
 * 项目方裁决「移除主题设置功能,只给类似首页的明暗调整即可」。
 * 上游原本有五个可调轴(preset / font / radius / scale / contentLayout),
 * 每个轴一份 cookie、一个 data-* 属性、一组调色板控件。现在只剩一个固定预设。
 *
 * 【为什么是删掉读取逻辑而不是只藏控件】
 * 五个轴各有一份独立 cookie。仅仅把控件从抽屉里拿掉,浏览器里那份陈旧的
 * `theme_font=serif` / `theme_scale=xl` 仍会被读出来写进 <body>,用户既看不到
 * 控件也改不回去 —— "看不见但还在起作用"比不删更糟。因此 THEME_COOKIE_KEYS、
 * 五个取值集合、resolveThemeFont 等【读取路径整体删除】,而不是把控件藏起来。
 * 陈旧 cookie 从此没有任何读取方,自然失效。
 *
 * 【为什么 THEME_PRESETS 原样保留】
 * 它是上游文件里的上游数据,别处(以及日后合并上游)可能依赖;删掉只会换来
 * 合并冲突。它现在的作用是给 {@link FIXED_THEME_PRESET } 提供编译期类型 ——
 * 预设名一旦拼错,类型检查立刻报错。
 */

export const THEME_PRESETS = [
  {
    value: 'default',
    name: 'Default',
    swatches: ['oklch(0.72 0.18 250)', 'oklch(0.7 0.12 280)'],
  },
  {
    // Inspired by Anthropic's official brand language: warm cream canvas
    // (#faf9f5) paired with clay/coral (#d97757) as the single accent.
    // Swatches preview the canvas → accent gradient that defines the system.
    value: 'anthropic',
    name: 'Anthropic',
    swatches: ['oklch(0.984 0.005 95)', 'oklch(0.685 0.142 38)'],
  },
  {
    // 编辑感暖色系:米白纸面 + 锈橙强调,深色版翻转为暖近黑 + 偏红强调。
    // 色值由参考稿的十六进制精确换算,定义在 styles/qy-sg-tokens.css。
    value: 'steins-gate',
    name: 'Steins Gate',
    swatches: ['oklch(0.959 0.016 86.4)', 'oklch(0.575 0.157 41.5)'],
  },
  {
    value: 'simple-large',
    name: 'Simple Large-font',
    swatches: ['oklch(0.15 0 0)', 'oklch(0.99 0 0)'],
  },
  {
    value: 'underground',
    name: 'Underground',
    swatches: ['oklch(0.5315 0.0694 156.19)', 'oklch(0.5748 0.0862 336.52)'],
  },
  {
    value: 'rose-garden',
    name: 'Rose Garden',
    swatches: ['oklch(0.5827 0.2418 12.23)', 'oklch(0.8131 0.1129 5.67)'],
  },
  {
    value: 'lake-view',
    name: 'Lake View',
    swatches: ['oklch(0.765 0.177 163.22)', 'oklch(0.551 0.0899 200.52)'],
  },
  {
    value: 'sunset-glow',
    name: 'Sunset Glow',
    swatches: ['oklch(0.5591 0.1882 25.33)', 'oklch(0.7938 0.1248 42.42)'],
  },
  {
    value: 'forest-whisper',
    name: 'Forest Whisper',
    swatches: ['oklch(0.5276 0.1072 182.22)', 'oklch(0.5236 0.0505 250.18)'],
  },
  {
    value: 'ocean-breeze',
    name: 'Ocean Breeze',
    swatches: ['oklch(0.5461 0.2152 262.88)', 'oklch(0.5854 0.2041 277.12)'],
  },
  {
    value: 'lavender-dream',
    name: 'Lavender Dream',
    swatches: ['oklch(0.5709 0.1808 306.89)', 'oklch(0.811 0.0589 201.14)'],
  },
] as const

export type ThemePreset = (typeof THEME_PRESETS)[number]['value']

/**
 * 全站唯一生效的预设。
 *
 * 它不是「默认值」而是「常量」：调色板里已经没有换预设的入口，cookie 也不再
 * 被读取，所以这个值就是每一位访客看到的东西。
 */
export const FIXED_THEME_PRESET: ThemePreset = 'steins-gate'

/**
 * `<body>` 上承载预设的属性名。
 *
 * **绝不能不写。** 整套 Steins Gate 主题 CSS（`styles/qy-sg-*.css`，一千三百
 * 多行）全部挂在 `[data-theme-preset='steins-gate']` 作用域下，属性一丢主题
 * 整个失效，页面会退回上游裸样式。
 *
 * 常量收在这里而不是各写各的字面量：写入方是 `ThemeCustomizationProvider`，
 * 读取方是 `features/qy/hooks/use-qy-theme-preset.ts`，两处各写一遍字符串就是
 * 同一个概念的第二份拷贝，改错一边只会表现为「样式在但区段头没了」。
 */
export const THEME_PRESET_ATTRIBUTE = 'data-theme-preset'

/**
 * 主题定制的取值。
 *
 * 只剩两个字段，都是常量：`preset` 恒为 {@link FIXED_THEME_PRESET}，
 * `radius` 恒为 `'default'`（上游 `[data-theme-radius=…]` 那一组覆盖块从此
 * 永不匹配，圆角由 `styles/qy-sg-tokens.css` 里的预设块决定）。
 *
 * 字段没有被一并删掉，是因为上游三张图表（dashboard 的 model-charts /
 * consumption-distribution-chart、pricing 的 model-details-charts）拿
 * `` `${preset}:${radius}` `` 当重新测量圆角的刷新键。保留常量让那三处继续
 * 编译且行为正确（键恒定 = 不必重测），比改上游文件更不容易在合并时冲突。
 */
export type ThemeCustomization = {
  preset: ThemePreset
  radius: 'default'
}

export const DEFAULT_THEME_CUSTOMIZATION: ThemeCustomization = {
  preset: FIXED_THEME_PRESET,
  radius: 'default',
}
