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
 * 未保存草稿的主题预览。
 *
 * **预览的作用域是「本标签页的这一次访问」，仅此而已。**
 * 它只写 `<body>` 的 `data-theme-preset` 属性，绝不碰 cookie，也绝不碰
 * `qy_site_theme_preset` / `qy_site_theme_force` 这两个 localStorage 键 ——
 * 后者是 `features/qy/lib/site-theme.ts` 用来在首屏同步决定主题的缓存，
 * 往里写一个还没保存的草稿等于：管理员随手拨了下拉、没点保存就走开，
 * 从此这台机器每次打开站点都用那个草稿主题，而后台显示的仍是旧值。
 * 更糟的是 `force_preset` 缓存被污染后连用户自己的 cookie 偏好都会被忽略。
 *
 * 也正因为只改 DOM 属性，预览天然影响不到任何其他访客：
 * 站点默认只有在 PUT 成功、`/api/qy/config` 重新下发之后才会外传。
 *
 * 属性口径必须与 `context/theme-customization-provider.tsx` 完全一致：
 * 上游默认预设对应「移除属性」而不是 `data-theme-preset="default"`，
 * 因为 `styles/theme-presets.css` 里根本没有 `[data-theme-preset='default']`
 * 这条选择器，写进去会得到一个没有任何预设变量的半成品页面。
 */

/** 预览目标。收窄成两个方法而不是 `HTMLElement`，便于直接单测。 */
export type QyPreviewTarget = Pick<
  HTMLElement,
  'removeAttribute' | 'setAttribute'
>

export const QY_PRESET_ATTRIBUTE = 'data-theme-preset'

/**
 * 把 `preset` 应用到 `target` 上。
 *
 * `upstreamDefault` 由调用方传入上游的 `DEFAULT_THEME_CUSTOMIZATION.preset`，
 * 不在这里 import：那个常量若改名，编译期就会在调用点报错。
 */
export function qyApplyPresetPreview(
  target: QyPreviewTarget,
  preset: string,
  upstreamDefault: string
): void {
  if (preset === upstreamDefault) {
    target.removeAttribute(QY_PRESET_ATTRIBUTE)
    return
  }
  target.setAttribute(QY_PRESET_ATTRIBUTE, preset)
}
