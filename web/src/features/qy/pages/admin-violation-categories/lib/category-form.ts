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

import type {
  QyViolationCategory,
  QyViolationCategoryInput,
} from '../types'

/** 表单态。数字一律走字符串：受控 number 输入清空时会得到 NaN。 */
export type QyCategoryFormValues = {
  id: number
  key: string
  name: string
  remark: string
  public_title: string
  public_desc: string
  published: boolean
  enabled: boolean
  window_hours: string
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
    // 新建默认**不公示**：公示文案还没写,先亮出来只会给用户一行空白。
    published: false,
    // 阈值默认启用但为 0 —— 0 表示这一类不单独触发处置。与后端种子同口径:
    // 新建一个类型绝不该顺手给它一条没人设定过的封号线。
    enabled: true,
    window_hours: '24',
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
    published: cat.published,
    enabled: cat.enabled,
    window_hours: String(cat.window_hours),
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
    published: values.published,
    enabled: values.enabled,
    window_hours: Number(values.window_hours) || 0,
    threshold: Number(values.threshold) || 0,
    sort_order: Number(values.sort_order) || 0,
    confirm,
  }
}

/** key 的取值域必须与后端 validateCategory 逐字一致。 */
const KEY_RE = /^[a-z0-9_-]+$/

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
  if (values.name.trim() === '') return t('qy_vcat_err_name_required')
  if (values.published && values.public_title.trim() === '') {
    return t('qy_vcat_err_public_title_required')
  }
  const window = Number(values.window_hours)
  if (!Number.isFinite(window) || window < 1) {
    return t('qy_vcat_err_window_range')
  }
  const threshold = Number(values.threshold)
  if (!Number.isFinite(threshold) || threshold < 0) {
    return t('qy_vcat_err_threshold_range')
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
  const window = Number(values.window_hours) || 0
  if (!values.enabled || threshold <= 0) return false
  if (before == null) return true
  if (!before.enabled || before.threshold <= 0) return true
  return threshold < before.threshold || window > before.window_hours
}
