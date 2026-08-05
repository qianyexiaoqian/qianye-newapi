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
import { qyPost } from '../lib/api'

/**
 * 渠道批量操作的取数层（`qianye/modules/channelops`）。
 *
 * 与上游 `features/channels/api.ts` 里那三个批量接口最大的差别是**返回值形状**：
 * 上游一律只回一个整数 count，于是"选了 20 个、第 7 个失败了"在协议层就说不出来。
 * 这里的每个接口都回 {@link QyChannelBatchResult}，逐条带着结局与原因。
 */

/** 单条渠道的结局。三档，不是成功/失败两分 —— 见 {@link QyChannelBatchItem}。 */
export type QyChannelBatchOutcome = 'failed' | 'ok' | 'skipped'

/**
 * 单条渠道的执行结果。
 *
 * `skipped` 这一档是这套接口存在的一半理由：上游把"库里本来就是目标状态"
 * 和"真的失败了"一起算成"没改动"，前端再拿 `ids.length - count` 一减，
 * 于是全站渠道本来就全开时点一次「批量启用」会显示成「N 个渠道启用失败」。
 */
export type QyChannelBatchItem = {
  id: number
  name: string
  outcome: QyChannelBatchOutcome
  /** 稳定标识，映射到 i18n 文案。仅 `failed` 与 `skipped` 有。 */
  code?: string
  /** 后端中文兜底，仅当 `code` 未被前端登记时展示。 */
  detail?: string
}

/**
 * 一次批次的完整报告。
 *
 * 后端保证 `total === succeeded + skipped + failed`，`items.length === total`。
 * 这条恒等式是界面唯一能信的东西：少了它，"20 选中、18 成功"就回答不了
 * 剩下 2 个要不要处理。
 */
export type QyChannelBatchResult = {
  total: number
  succeeded: number
  skipped: number
  failed: number
  items: QyChannelBatchItem[]
}

/** 批量启用 / 手动停用。后端只接受这两个目标状态。 */
export function qyBatchChannelStatus(
  ids: number[],
  status: number
): Promise<QyChannelBatchResult> {
  return qyPost<QyChannelBatchResult>('/admin/channels/batch-status', {
    ids,
    status,
  })
}

/** 批量删除。每个渠道各自一个事务，渠道行与 abilities 行同生共死。 */
export function qyBatchDeleteChannels(
  ids: number[]
): Promise<QyChannelBatchResult> {
  return qyPost<QyChannelBatchResult>('/admin/channels/batch-delete', { ids })
}

/**
 * 批量重置本站统计。
 *
 * 两个开关都必须显式传，后端不给默认值：这是一个抹掉历史统计的操作，
 * "请求里没说要清什么"唯一安全的解释是"什么都别清"。两个都是 false 时后端
 * 整批 400，不会静默变成一次回绿灯的空操作。
 */
export function qyBatchResetChannelUsage(
  ids: number[],
  options: { resetBalance: boolean; resetUsedQuota: boolean }
): Promise<QyChannelBatchResult> {
  return qyPost<QyChannelBatchResult>('/admin/channels/batch-reset-usage', {
    ids,
    reset_used_quota: options.resetUsedQuota,
    reset_balance: options.resetBalance,
  })
}

/**
 * 单条结局码 → i18n key。
 *
 * 与 `qianye/modules/channelops/errors.go` 的常量逐字对应。未登记的 code
 * 回落到后端 `detail`，再回落到一句泛化文案 —— 新增后端 code 不会让某一行变空白。
 */
export const QY_CHANNEL_BATCH_ITEM_I18N: Record<string, string> = {
  qy_chops_item_not_found: 'qy_chops_item_not_found',
  qy_chops_item_db_error: 'qy_chops_item_db_error',
  qy_chops_item_aborted: 'qy_chops_item_aborted',
  qy_chops_item_no_change: 'qy_chops_item_no_change',
  // 状态没变、而后端一行日志都没有 —— 多节点部署下本节点缓存与库不一致的
  // 那一档。它与 db_error 分开是因为下一步动作完全不同："等一个缓存同步周期
  // 再重试"对上，"去查后端日志"会把管理员支去翻一份空日志。
  qy_chops_item_not_applied: 'qy_chops_item_not_applied',
}
