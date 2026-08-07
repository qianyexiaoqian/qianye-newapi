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
 * 分档输入框的三态草稿。
 *
 * # 为什么不能直接用一个字符串
 *
 * 全站门槛那一页的草稿是 `Record<string, string>`：每一项都必然有值，空串就是
 * 非法。分档不一样，它有三种状态，而其中两种在界面上都可能显示成一个空框：
 *
 * | 状态     | 存储      | 界面                       |
 * |----------|-----------|----------------------------|
 * | 未覆盖   | `null`    | 复选框不勾，输入框收起      |
 * | 覆盖为 0 | `0`       | 复选框勾上，输入框里是 `0`  |
 * | 覆盖为 n | `n`       | 复选框勾上，输入框里是 n    |
 *
 * 用「空串 = 未覆盖」表达的话，运营清空一个输入框想改成 0，得到的是「跟随
 * 全局」——**方向完全相反**：前者是把闸门关掉，后者是把闸门交还给全站的值。
 * 所以「覆不覆盖」必须由一个独立的布尔承载，而不是从文本推断。
 *
 * # USD 往返一致性
 *
 * 金额项（`unit === 'quota'`）在界面上按 USD 录入，存储仍是额度整数，换算复用
 * 全站门槛那一页的 {@link qyQuotaDraftText} / {@link qyQuotaDraftValue}
 * （整数运算、往返无损，见 `lib/quota-usd.ts`）。
 *
 * 这一对函数就是「界面显示 USD → 运营不改直接保存 → 存回的额度逐位相同」
 * 这条不变量在**分档**上的落点：差一个 quota，运营什么都没改点一下保存，
 * 审计里就多出一条无中生有的门槛变更，而门槛变更正是「谁把某一档的日额度
 * 放大了十倍」要追的那条线索。
 */
import {
  qyQuotaDraftText,
  qyQuotaDraftValue,
  type QyUsdScale,
} from '../../../lib/quota-usd'

/** 一项的草稿：覆不覆盖(独立的布尔) + 覆盖时的输入文本。 */
export type QyTierFieldDraft = {
  /** 勾上表示这一档要覆盖这一项。取消勾选 = 回落全站门槛。 */
  covered: boolean
  /**
   * 输入框文本。`covered` 为 false 时它仍然保留上一次的内容 ——
   * 运营取消勾选之后又改主意勾回来，不该发现自己刚才填的数字没了。
   */
  text: string
}

/**
 * 一项草稿解析后的结果。
 *
 * 三个取值一一对应三种状态,`invalid` 与 `unset` 严格分开:前者要标红并挡住
 * 保存,后者是一个完全合法的选择。把它们塌缩成同一个 `null`,
 * 一个填错的输入框就会被静默当成「不覆盖」保存出去。
 */
export type QyTierFieldValue =
  | { kind: 'unset' }
  | { kind: 'value'; quota: number }
  | { kind: 'invalid' }

/** 存储值(`number | null | undefined`)→ 草稿。 */
export function qyTierDraftFrom(
  stored: number | null | undefined,
  scale: QyUsdScale,
  asUsd: boolean
): QyTierFieldDraft {
  if (stored == null) return { covered: false, text: '' }
  return { covered: true, text: qyQuotaDraftText(stored, scale, asUsd) }
}

/** 草稿 → 解析结果。与 {@link qyTierDraftFrom} 严格互逆。 */
export function qyTierDraftValue(
  draft: QyTierFieldDraft,
  scale: QyUsdScale,
  asUsd: boolean
): QyTierFieldValue {
  if (!draft.covered) return { kind: 'unset' }
  const quota = qyQuotaDraftValue(draft.text, scale, asUsd)
  if (quota == null) return { kind: 'invalid' }
  return { kind: 'value', quota }
}

/**
 * 解析结果 → 请求体里的那一项。
 *
 * `invalid` 在这里返回 `null` 只是类型上的收尾 —— 调用方必须在提交**之前**
 * 就用 {@link qyTierDraftHasInvalid} 挡住保存。让一个填错的框走到这里,
 * 它就会被静默存成「不覆盖」,而那是一次没人察觉的门槛放宽。
 */
export function qyTierOverrideOf(value: QyTierFieldValue): number | null {
  return value.kind === 'value' ? value.quota : null
}

/** 整档草稿里有没有填错的项。 */
export function qyTierDraftHasInvalid(values: QyTierFieldValue[]): boolean {
  return values.some((value) => value.kind === 'invalid')
}

/** 整档草稿里至少覆盖了一项吗 —— 一项都不覆盖与不配置完全等价,后端会 400。 */
export function qyTierDraftCoversAny(values: QyTierFieldValue[]): boolean {
  return values.some((value) => value.kind === 'value')
}
