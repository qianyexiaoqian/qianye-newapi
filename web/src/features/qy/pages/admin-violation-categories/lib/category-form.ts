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

import {
  QY_WINDOW_UNLIMITED,
  qyWindowIsUnlimited,
  qyWindowWidens,
} from '../../../lib/violation-thresholds'
import type { QyViolationCategory, QyViolationCategoryInput } from '../types'

/** 表单态。数字一律走字符串：受控 number 输入清空时会得到 NaN。 */
export type QyCategoryFormValues = {
  id: number
  key: string
  name: string
  remark: string
  public_title: string
  public_desc: string
  ai_guidance: string
  ai_excluded: boolean
  published: boolean
  enabled: boolean
  /**
   * 小时数输入框的内容。**不限期限时它不参与提交**，由 `window_unlimited` 接管。
   *
   * 两个字段在表单态里并存、在提交时折成一个数字，是刻意的：勾上不限期限之后
   * 用户填过的小时数必须留在框里，取消勾选时要能原样回来。若表单态也只留一个
   * 数字，取消勾选就只能回落到一个硬编码的 24 —— 那会把管理员刚填的 72 吃掉。
   * 折叠只发生在 `qyCategoryFormToPayload` 一处，所以库里永远只有一个事实。
   */
  window_hours: string
  window_unlimited: boolean
  threshold: string
  sort_order: string
}

export function qyEmptyCategoryForm(): QyCategoryFormValues {
  return {
    id: 0,
    key: '',
    name: '',
    remark: '',
    public_title: '',
    public_desc: '',
    ai_guidance: '',
    // 新建默认**参与** AI 审核:运营新建一个类型时想的就是"让 AI 也认这一类"。
    // 排除是例外(判据不是文本,例如按频率判的蒸馏),不是默认。
    ai_excluded: false,
    // 新建默认**不公示**：公示文案还没写,先亮出来只会给用户一行空白。
    published: false,
    // 阈值默认启用但为 0 —— 0 表示这一类不单独触发处置。与后端种子同口径:
    // 新建一个类型绝不该顺手给它一条没人设定过的封号线。
    enabled: true,
    window_hours: '24',
    // 新建默认**有窗口**。不限期限是一档需要人主动选的收紧配置：它让一年前的
    // 命中永远算数，绝不该是某人新建类型时顺手带上的默认值。
    window_unlimited: false,
    threshold: '0',
    sort_order: '100',
  }
}

export function qyCategoryToForm(
  cat: QyViolationCategory
): QyCategoryFormValues {
  return {
    id: cat.id,
    key: cat.key,
    name: cat.name,
    remark: cat.remark,
    public_title: cat.public_title,
    public_desc: cat.public_desc,
    ai_guidance: cat.ai_guidance,
    ai_excluded: cat.ai_excluded,
    published: cat.published,
    enabled: cat.enabled,
    // 不限期限的行没有小时数可回填，给框里留一个 24 —— 那是取消勾选时最不意外的
    // 落点，也是这一列的出厂值。绝不回填 -1：它会以 `-1` 的形态出现在输入框里。
    window_hours: qyWindowIsUnlimited(cat.window_hours)
      ? '24'
      : String(cat.window_hours),
    window_unlimited: qyWindowIsUnlimited(cat.window_hours),
    threshold: String(cat.threshold),
    sort_order: String(cat.sort_order),
  }
}

export function qyCategoryFormToPayload(
  values: QyCategoryFormValues,
  confirm: boolean
): QyViolationCategoryInput {
  return {
    id: values.id,
    key: values.key.trim().toLowerCase(),
    name: values.name.trim(),
    remark: values.remark,
    public_title: values.public_title.trim(),
    public_desc: values.public_desc,
    ai_guidance: values.ai_guidance,
    ai_excluded: values.ai_excluded,
    published: values.published,
    enabled: values.enabled,
    // 两个表单字段在这里折成库里那一列。勾了不限期限就发哨兵，小时数一律忽略 ——
    // 发一个"既是 -1 又是 72"的组合是不可能的，因为库里只有一列。
    window_hours: values.window_unlimited
      ? QY_WINDOW_UNLIMITED
      : Number(values.window_hours) || 0,
    threshold: Number(values.threshold) || 0,
    sort_order: Number(values.sort_order) || 0,
    confirm,
  }
}

/** key 的取值域必须与后端 validateCategory 逐字一致。 */
const KEY_RE = /^[a-z0-9_-]+$/

/**
 * `none` 是 AI 审核里"未违规"的取值,不是一个违规类型。后端 validateCategory
 * 拒绝它 —— 建成类型的话,发给模型的清单里会同时出现"none = 未违规"与
 * "none = 某个违规类型",而模型两边都对不上。
 */
const QY_VCAT_RESERVED_KEY = 'none'

/** 判定说明的字数上限,与后端 categoryAIGuidanceMax 同值。 */
const QY_VCAT_AI_GUIDANCE_MAX = 256

/**
 * 前端校验。它**不是**权限判断，后端会再校验一遍 —— 它只是不让管理员白按一次。
 *
 * 三条与后端严格对齐：key 取值域、名称必填、以及最重的那条
 * 「勾了公示就必须填公示标题」。最后一条放过的话，用户端会看到一行空白标题，
 * 而下一个人最省事的修法是回落到内部名 —— 那正是这套隔离要防的事。
 */
export function qyValidateCategoryForm(
  values: QyCategoryFormValues,
  t: TFunction
): string | null {
  const key = values.key.trim().toLowerCase()
  if (key === '') return t('qy_vcat_err_key_required')
  if (!KEY_RE.test(key)) return t('qy_vcat_err_key_charset')
  if (key === QY_VCAT_RESERVED_KEY) return t('qy_vcat_err_key_reserved')
  if (values.name.trim() === '') return t('qy_vcat_err_name_required')
  if (values.published && values.public_title.trim() === '') {
    return t('qy_vcat_err_public_title_required')
  }
  // 勾了不限期限时小时数框不参与提交，也就不该拦住保存 —— 拦下去的话，
  // 一个清空了小时数、只想选"不限期限"的管理员会看到一句他改不掉的报错。
  if (!values.window_unlimited) {
    const window = Number(values.window_hours)
    if (!Number.isFinite(window) || window < 1) {
      return t('qy_vcat_err_window_range')
    }
  }
  const threshold = Number(values.threshold)
  if (!Number.isFinite(threshold) || threshold < 0) {
    return t('qy_vcat_err_threshold_range')
  }
  // 判定说明的上限比内部说明窄一半(后端 categoryAIGuidanceMax = 256):
  // 它是**每一次审核调用都要付一遍**的 token,而类型条数由运营决定、没有上界。
  if (values.ai_guidance.length > QY_VCAT_AI_GUIDANCE_MAX) {
    return t('qy_vcat_err_ai_guidance_len', { max: QY_VCAT_AI_GUIDANCE_MAX })
  }
  return null
}

/**
 * 这次编辑会不会**扩大处置面**。判据与后端 categoryTightens 同形。
 *
 * 用途只有一个：决定要不要在保存前拉一次影响面预览并弹二次确认。判错方向的后果
 * 不对称 —— 漏判会让一批存量账号在管理员毫不知情的情况下越线，误判只是多点一次。
 * 所以新建一律算收紧。
 */
export function qyCategoryTightens(
  before: QyViolationCategory | null,
  values: QyCategoryFormValues
): boolean {
  const threshold = Number(values.threshold) || 0
  // 窗口先折成库里那一列的形状再比：勾了不限期限就是哨兵。
  // 直接拿输入框里的小时数比大小，会让「24 → 不限期限」看起来毫无变化。
  const window = values.window_unlimited
    ? QY_WINDOW_UNLIMITED
    : Number(values.window_hours) || 0
  if (!values.enabled || threshold <= 0) return false
  if (before == null) return true
  if (!before.enabled || before.threshold <= 0) return true
  return (
    threshold < before.threshold || qyWindowWidens(before.window_hours, window)
  )
}
