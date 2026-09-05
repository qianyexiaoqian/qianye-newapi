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
import type { TFunction } from 'i18next'

/**
 * 星屑金额的格式化。
 *
 * 星屑是**整数积分**，不是额度：1 星屑 = `stardust.quota_per_unit` 额度，但那个
 * 换算只在后端结算时发生一次，前端从头到尾只见整数。所以这里**绝不**转调
 * `lib/format.ts`（那一套是额度→USD→展示币种的链路）：把 500 星屑印成 `$0.001`
 * 是本轮改造要消灭的那种错误 —— 用户看到的钱和他账上那个整数对不上号。
 *
 * 千分位用固定的英文逗号而不是 `toLocaleString()`：后者的输出随运行环境的
 * locale 变化（Node 精简 ICU、不同浏览器），测试与界面会各说各的。
 */

/** 整数 → 带千分位的字符串。非有限数返回 `-`，小数被截断（星屑本来就没有小数）。 */
export function formatSd(amount: number | null | undefined): string {
  if (amount == null || !Number.isFinite(amount)) return '-'
  const int = Math.trunc(amount)
  const digits = String(Math.abs(int)).replaceAll(/\B(?=(\d{3})+(?!\d))/g, ',')
  return int < 0 ? `-${digits}` : digits
}

/** 同 {@link formatSd}，正数带 `+` 前缀。收支双向的流水列表用它。 */
export function formatSdSigned(amount: number | null | undefined): string {
  const text = formatSd(amount)
  if (text === '-') return text
  return amount != null && Math.trunc(amount) > 0 ? `+${text}` : text
}

/**
 * 「数字 + 单位」的一段文本：`1,234 星屑`。
 *
 * 给**要插进句子里**的金额用（`t('…', { amount })`、统计卡片的 `value`），能放
 * 组件的地方一律用 `QySdAmount`。单位由调用方从 `useStardustName()` 拿 ——
 * 这里不读配置，保持纯函数。
 */
export function formatSdWithUnit(
  amount: number | null | undefined,
  unit: string
): string {
  const text = formatSd(amount)
  return text === '-' ? text : `${text} ${unit}`
}

/**
 * 星屑的单位名。运营在星屑配置里改过就用那个词，没配（空白）时回落到 i18n 的
 * 默认词 —— 回落词随语言切换，所以它必须在渲染期取，不能烤进配置快照。
 */
export function qyStardustName(name: string, t: TFunction): string {
  const trimmed = name.trim()
  return trimmed === '' ? t('qy_sd_unit_default') : trimmed
}
