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
  QyViolationBatchResult,
  QyViolationBatchScopeOp,
  QyViolationRule,
} from '../types'

/**
 * 规则列表多选批量操作的取舍与算式。
 *
 * 这里只放**纯函数**：影响面怎么算、二次确认要说什么、批次结果算成功还是失败。
 * 放进组件里的话，「批量启用会把几条真实模式规则送上线」这种判断就只能靠打开
 * 浏览器点一遍来验证，而它恰恰是这一页最贵的一个数字。
 */

/** 单次批量的 id 上限，与后端 `maxRuleBatchIds` 同口径。 */
export const QY_VIOLATION_BATCH_MAX = 200

/** 批量作用分组的三种写法，顺序即界面上的顺序。 */
export const QY_VIOLATION_BATCH_SCOPE_OPS: QyViolationBatchScopeOp[] = [
  'append',
  'replace',
  'remove',
]

/**
 * 这次批量启用会把哪几条规则送进**真实执行**。
 *
 * 与后端 `pendingEnforceRules` 同一个判据：`mode === 'enforce'` 且**当前是停用**。
 * 刻意只算这一档，而不是「选中里所有 enforce 规则」：一条已经启用的 enforce 规则
 * 再点一次启用是彻底的空操作，把它算进警告数字里只会让数字虚高，而虚高的警告
 * 会训练人闭着眼睛点确认。
 *
 * 后端对未知 / 空 mode 一律按影子处理（判据是 `mode === 'enforce'`），前端必须同向：
 * 反过来会在一个安全的动作上弹一个吓人的框，而那同样是在训练人无视警告。
 */
export function qyEnforceRulesPendingEnable(
  rules: QyViolationRule[]
): QyViolationRule[] {
  return rules.filter((rule) => rule.mode === 'enforce' && !rule.enabled)
}

/**
 * 这次批量启停真正会改动几条规则。
 *
 * 二次确认里必须同时说出「选中 N 条」与「其中 M 条会真的改变」：一次「全选 → 批量
 * 启用」里绝大多数规则本来就是启用的，只报选中数会让人以为自己在做一件比实际大
 * 得多的事，而下一秒的结果报告里那些规则会落进 `skipped` —— 两个数字对不上，
 * 人就不再相信这个确认框。
 */
export function qyBatchEnableChangeCount(
  rules: QyViolationRule[],
  next: boolean
): number {
  return rules.filter((rule) => rule.enabled !== next).length
}

/**
 * 批次结果该报成什么颜色。
 *
 * 判据是**响应体里的分档计数**，不是 HTTP 状态码 —— 后端整批一律 200（逐条明细
 * 才是这个接口的产品，`success:false` 会让 qy 的 unwrap 把 data 整个丢掉）。
 * 所以「接口成功」不等于「事情做成了」，一条都没改成时必须是红的。
 *
 *   `error`    有失败项。哪几条、为什么，必须摊开给人看
 *   `warning`  没有失败，但一条都没改动（全是"本来就是目标状态"）——
 *              这通常意味着选错了范围，报绿色会让人以为改好了
 *   `success`  真的改动了，且没有失败
 */
export function qyBatchResultTone(
  result: QyViolationBatchResult
): 'error' | 'success' | 'warning' {
  if (result.failed > 0) return 'error'
  if (result.succeeded === 0) return 'warning'
  return 'success'
}

/** 结果里需要单独摊开的那些条目（失败与跳过），成功项不占屏幕。 */
export function qyBatchNoteworthyItems(result: QyViolationBatchResult) {
  return result.items.filter((item) => item.outcome !== 'ok')
}

/**
 * 逐条结果码 → i18n key。
 *
 * 未登记的 code 回落到后端的中文 `detail`（见 `types.ts`），所以后端新增一个码
 * 不会让界面显示空白 —— 但也不会被翻译，登记是首选。
 */
export const QY_VIOLATION_BATCH_ITEM_I18N: Record<string, string> = {
  qy_vio_batch_item_not_found: 'qy_vio_batch_item_not_found',
  qy_vio_batch_item_no_change: 'qy_vio_batch_item_no_change',
  qy_vio_batch_item_wont_compile: 'qy_vio_batch_item_wont_compile',
  qy_vio_batch_item_scope_too_long: 'qy_vio_batch_item_scope_too_long',
  qy_vio_batch_item_direction_mismatch: 'qy_vio_batch_item_direction_mismatch',
  qy_vio_batch_item_enforce_ack: 'qy_vio_batch_item_enforce_ack',
  qy_vio_batch_item_stale: 'qy_vio_batch_item_stale',
  qy_vio_batch_item_db_error: 'qy_vio_batch_item_db_error',
}
