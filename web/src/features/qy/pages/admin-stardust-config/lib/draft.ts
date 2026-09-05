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
import type {
  QyStardustAdminConfig,
  QyStardustBound,
  QyStardustConfigPatch,
} from '../types'

/**
 * 星屑配置页的草稿逻辑：文本 → 存储值、区间判定、以及「只提交改动键」的 diff。
 *
 * 抽成纯函数是因为**只 PUT 改动键**是这一页与后端之间的契约（审计里每一条
 * 配置变更都该是真的变了），而 diff 写在组件里只能靠挂整棵树才测得到。
 */

/** 单位名的字符上限，与后端 `settings.go` 的 `maxNameRunes` 同值。 */
export const QY_SD_NAME_MAX_RUNES = 16

/** `qy_settings` 里存 `0` / `1` 的布尔键。界面上渲染成开关。 */
export const QY_SD_BOOLEAN_KEYS: ReadonlySet<string> = new Set(['show_entry'])

/** 唯一的字符串键：货币名。其余全是整数。 */
export const QY_SD_NAME_KEY = 'name'

/**
 * 受支付合规门约束的三个邀请类键（后端 `isInviteKey`）。
 * 合规未确认时后端对它们的正值 400 `qy_sd_compliance_required`，
 * 界面上要在保存之前就把这件事说出来 —— 输入框禁用 + 一句提示。
 */
export const QY_SD_INVITE_KEYS: ReadonlySet<string> = new Set([
  'invite_consume_bps',
  'invite_topup_bps',
  'invite_redeem_bps',
  'invite_register_stardust',
])

/**
 * 套餐返还的来源闭集，顺序与后端 `NormalizeSources` 的规范顺序一致。
 * `stardust`（商城自购）刻意不在里面：星屑买套餐再返星屑是自供回路（D-F）。
 */
export const QY_SD_PLAN_REWARD_SOURCES: readonly string[] = [
  'order',
  'balance',
  'admin',
  'redemption',
]

/** 金额键（单位是整数星屑）：按 `_stardust` 后缀判定，新增金额键时界面自动跟随。 */
export function qySdIsStardustKey(key: string): boolean {
  return key.endsWith('_stardust')
}

/** 万分比键：按 `_bps` 后缀判定。界面上把它换算成百分数摆在旁边。 */
export function qySdIsBpsKey(key: string): boolean {
  return key.endsWith('_bps')
}

/**
 * 一个取值是否落在后端下发的区间里。
 *
 * 契约的区间写的是 `lo` / `hi`（不是抽奖那边的 `min` / `max`），两端都是闭区间，
 * 且**两端都必然存在** —— 与后端 `Bound.Contains` 同一条判定。
 */
export function qySdBoundContains(
  bound: QyStardustBound,
  value: number
): boolean {
  return value >= bound.lo && value <= bound.hi
}

/** 草稿文本 → 非负整数。空串、非数字、超出安全整数一律 `null`。 */
export function qySdParseDraftText(raw: string): number | null {
  const text = raw.trim()
  if (text === '' || !/^\d+$/.test(text)) return null
  const value = Number(text)
  return Number.isSafeInteger(value) ? value : null
}

/** 按键读取生效值。未知键返回 `''` —— 后端加了新键而前端还没认识它时不该崩。 */
export function qySdEffectiveValue(
  config: QyStardustAdminConfig,
  key: string
): number | string {
  const record = config.effective as unknown as Record<
    string,
    number | string | undefined
  >
  return record[key] ?? ''
}

/**
 * 草稿文本 → 存储值，并按后端下发的区间 / 单位名规则卡一次。非法返回 `null`。
 *
 * 单位名只做与后端 `validName` 相同的两条：去空白后非空、不超过 16 个字符
 * （按 code point 数，与 `utf8.RuneCountInString` 同口径）。
 */
export function qySdParseDraft(
  config: QyStardustAdminConfig,
  key: string,
  raw: string
): number | string | null {
  if (key === QY_SD_NAME_KEY) {
    const name = raw.trim()
    if (name === '' || [...name].length > QY_SD_NAME_MAX_RUNES) return null
    return name
  }
  const value = qySdParseDraftText(raw)
  if (value == null) return null
  const bound = config.bounds[key]
  if (bound != null && !qySdBoundContains(bound, value)) return null
  return value
}

/** 一处改动的复述：键、旧值、新值。确认弹窗与 PUT 请求共用它。 */
export type QySdConfigChange = {
  key: string
  from: number | string
  to: number | string
}

/**
 * 草稿与生效值的差异，只含**真的变了且合法**的键。
 *
 * 比较发生在存储值上：草稿文本由 `String()` 生成、由 `qySdParseDraft` 读回，
 * 往返无损，所以运营什么都不改直接保存时这里一条都挑不出来，审计里也不会
 * 多出一条什么都没改的配置变更。非法草稿（`null`）不算改动 —— 它由
 * {@link qySdInvalidKey} 单独报出来挡住保存键。
 */
export function qySdConfigChanges(
  config: QyStardustAdminConfig,
  draft: Record<string, string>
): QySdConfigChange[] {
  const changes: QySdConfigChange[] = []
  for (const key of config.editable_keys) {
    const from = qySdEffectiveValue(config, key)
    const to = qySdParseDraft(config, key, draft[key] ?? '')
    if (to == null || to === from) continue
    changes.push({ key, from, to })
  }
  return changes
}

/** 第一个非法的草稿键；全部合法返回 `null`。 */
export function qySdInvalidKey(
  config: QyStardustAdminConfig,
  draft: Record<string, string>
): string | null {
  return (
    config.editable_keys.find(
      (key) => qySdParseDraft(config, key, draft[key] ?? '') == null
    ) ?? null
  )
}

/** 改动清单 → 稀疏 PUT 请求体。数字键发数字、单位名发字符串。 */
export function qySdConfigPatch(
  changes: QySdConfigChange[]
): QyStardustConfigPatch {
  return Object.fromEntries(changes.map((change) => [change.key, change.to]))
}
