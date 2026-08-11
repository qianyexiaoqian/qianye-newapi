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
import { api } from '@/lib/api'

import {
  qyDelete,
  qyErrorFromBlobFailure,
  qyGet,
  qyPatch,
  qyPost,
  qyPut,
  QY_API_PREFIX,
} from '../../lib/api'
import type { QyPage } from '../../lib/types'
import type {
  QyViolationBatchResult,
  QyViolationBatchScopeOp,
  QyViolationBreaker,
  QyViolationBuiltinCatalog,
  QyViolationCounterPage,
  QyViolationGroupScopeMode,
  QyViolationImportResult,
  QyViolationRecord,
  QyViolationRule,
  QyViolationRuleInput,
  QyViolationRuleTestResult,
  QyViolationStats,
} from './types'

export type QyViolationRuleListParams = {
  p: number
  page_size: number
  phase?: string
  keyword?: string
}

export function listQyViolationRules(
  params: QyViolationRuleListParams
): Promise<QyPage<QyViolationRule>> {
  return qyGet<QyPage<QyViolationRule>>('/admin/violation/rules', params)
}

export function createQyViolationRule(
  body: QyViolationRuleInput
): Promise<{ id: number }> {
  return qyPost<{ id: number }>('/admin/violation/rules', body)
}

export function updateQyViolationRule(
  id: number,
  body: QyViolationRuleInput
): Promise<unknown> {
  return qyPut<unknown>(`/admin/violation/rules/${id}`, body)
}

/**
 * 列表行内的快速启停 —— **只写 `enabled` 一列**。
 *
 * 刻意不复用 `updateQyViolationRule`：那个接口提交的是前端手上那一整份规则，
 * 而它是列表页在 15 秒 `staleTime` 里拉下来的拷贝。这期间同事改窄了 `pattern`、
 * 把 `mode` 从真实调回影子、改了作用域，都会被这次「我只是想关一下开关」原样
 * 覆盖回旧值 —— 一次没有人按下过的静默回滚，而回滚掉的正是决定谁被扣钱、
 * 谁被封号的那几列。后端同样只 `UPDATE enabled / updated_at / updated_by`。
 *
 * `changed=false` 表示这次调用什么都没改（重复点击，或别人抢先改成了同一个值）。
 * 后端在这条路径上不写审计、不 bump 规则版本号 —— 什么都没发生的一次调用不该在
 * 审计里留下一条「改过」。
 *
 * 启用一条**编译不过**的规则会被后端以 400 拒绝：规则快照对编译失败的规则是
 * 静默跳过的，放行的话就是「启用成功、界面显示已启用、线上永不命中」。
 */
export function setQyViolationRuleEnabled(
  id: number,
  enabled: boolean
): Promise<{ enabled: boolean; changed: boolean }> {
  return qyPatch<{ enabled: boolean; changed: boolean }>(
    `/admin/violation/rules/${id}/enabled`,
    { enabled }
  )
}

/**
 * 多选之后的批量启用 / 禁用。
 *
 * 与单条启停同一条纪律：后端只 `UPDATE enabled / updated_at / updated_by`，
 * 不碰 `mode` / `pattern` / 作用域。**批量不是 mode 的第二个入口** —— 把一批规则
 * 从影子切成真实，下一秒就开始真的扣费、阻断、累计封号，而批量入口看不到
 * pattern 与作用域这些做判断必需的上下文。改 mode 只能在单条编辑抽屉里。
 *
 * `ack_enforce` 是「我已经看到选中里有哪些真实模式的规则，并且确认要把它们打开」。
 * 只有 `enabled=true` 且选中里存在**当前停用的 enforce 规则**时后端才要求它；
 * 没带就是 400 `qy_vio_batch_enforce_ack_required`，附带那批规则的 id 与名字。
 * 前端在二次确认框里已经把这个数字摆出来了，所以正常路径上不会撞到这个 400 ——
 * 撞到就说明列表是旧的（别人刚把某条切成了真实），此时该做的是刷新后重新确认。
 *
 * 整批**一律 200**，逐条结局在 `items` 里。判据是响应体里的 `succeeded` /
 * `failed`，不是 HTTP 状态码。
 */
export function batchSetQyViolationRulesEnabled(body: {
  ids: number[]
  enabled: boolean
  ack_enforce: boolean
}): Promise<QyViolationBatchResult> {
  return qyPost<QyViolationBatchResult>(
    '/admin/violation/rules/batch/enabled',
    body
  )
}

/**
 * 多选之后的批量设置作用分组（**模型分组**维度 —— 判定比的是这次请求实际路由到的
 * `using_group`，不是「这个人是谁」）。
 *
 * `group_scope_mode` 是**必填**，三种写法都要。同一串分组名在 `include` 与
 * `exclude` 下含义完全相反：给一条 exclude 规则追加 `vip`，是**多豁免了一个分组**，
 * 而操作者以为自己多防了一个。所以 `append` / `remove` 遇到方向与请求不一致的规则
 * 一律拒做（逐条 `qy_vio_batch_item_direction_mismatch`），绝不替它翻向；
 * `replace` 不受此限 —— 「覆盖」这个词本身就包含了方向。
 *
 * 后端只 `UPDATE group_scope / group_scope_mode / updated_at / updated_by`。
 */
export function batchSetQyViolationRulesGroupScope(body: {
  ids: number[]
  op: QyViolationBatchScopeOp
  groups: string[]
  group_scope_mode: QyViolationGroupScopeMode
}): Promise<QyViolationBatchResult> {
  return qyPost<QyViolationBatchResult>(
    '/admin/violation/rules/batch/group-scope',
    body
  )
}

/** 软删：历史记录的 `rule_id` 指向规则，硬删会让申诉复核失去上下文。 */
export function deleteQyViolationRule(id: number): Promise<unknown> {
  return qyDelete<unknown>(`/admin/violation/rules/${id}`)
}

/**
 * 规则试跑。
 *
 * 这是本模块最重要的一个接口：没有它，管理员只能「改完上线看线上炸不炸」，
 * 而线上一炸就是全站用户被误扣误封。
 *
 * 入参按**规则的匹配维度**拆开，而不是一个万能样本框：试跑的目的是测这条规则的
 * 逻辑，所以它该问的是这条规则真正会读到的东西。面板只发当前规则会用到的字段，
 * 其余留空 —— 后端把这份判断（`inputs`）原样回给界面。
 */
export function testQyViolationRule(body: {
  rule: QyViolationRuleInput
  /** prompt 阶段扫的请求上下文文本。 */
  request_text: string
  /** 上游返回的错误正文。 */
  upstream_text: string
  /** 上游软违规原因（`openai_finish_reason=content_filter` 之类）。 */
  reject_reason: string
  /** 上游 HTTP 状态码。它同时是判据与作用域闸。 */
  status_code: number
  /** 上游错误码。 */
  error_code: string
  /** `request_rate` 规则的试跑输入：假设这一分钟内已有多少条非流式请求。 */
  rate_count: number
  /**
   * `ai_review` 规则的试跑输入：假设外部审核给出了这样一个结论。
   *
   * 空的 `ai_verdict` 表示这一条压根没送审（或审核失败/超时），也就是 AI 规则
   * **必然不命中**的那一档 —— 「失败即放行」在试跑里的表达，必须能重现。
   */
  ai_verdict: string
  ai_category: string
  /** 置信度走字符串：0.8 往返一次 JSON number 会变成 0.8000000000000000444，
   *  而它要跟规则的 ai_min_confidence 做大小比较。 */
  ai_confidence: string
  /** 作用域输入，可选：只影响「这条规则在不在作用域内」，不影响内容匹配。 */
  model: string
  group: string
}): Promise<QyViolationRuleTestResult> {
  return qyPost<QyViolationRuleTestResult>('/admin/violation/rules/test', body)
}

export function getQyViolationStats(params: {
  hours: number
}): Promise<QyViolationStats> {
  return qyGet<QyViolationStats>('/admin/violation/stats', params)
}

/**
 * 手动解除熔断。规则确认修正后才该点。
 *
 * 它**一个字节都不动规则的 mode** —— 解除熔断的语义是「刹车松开，各条规则回到
 * 它们自己声明的模式」。顺手把规则转正就是替运营做了一个他没做过的决定。
 *
 * 全局影子开关（`PUT /violation/mode`）已随全局模式层一起删除：模式绑在规则上，
 * 改模式就是去改那条规则，不再有第二个入口。
 */
export function resetQyViolationBreaker(): Promise<QyViolationBreaker> {
  return qyPost<QyViolationBreaker>('/admin/violation/breaker/reset')
}

/**
 * 内置防护规则包的目录 + 每条在本站点的导入状态。
 *
 * 返回的是**代码里的模板 + 库里的状态**，与规则列表不是同一种东西 ——
 * 所以它是独立的一条路由，而不是 `/rules?source=builtin`。
 */
export function getQyViolationBuiltinCatalog(): Promise<QyViolationBuiltinCatalog> {
  return qyGet<QyViolationBuiltinCatalog>('/admin/violation/rules/builtin')
}

/**
 * 一键导入内置防护规则包。
 *
 * `keys` 为空 = 全部。导入出来一律是**影子模式**的普通规则行，可编辑、可删除、
 * 可单独开关。`upgrade: true` 时额外把「未被运营改过」的旧版规则替换成新版
 * 模式串 —— 改过的一律跳过并在 `results` 里如实说明，任何情况下都不覆盖。
 */
export function importQyViolationBuiltinRules(body: {
  keys?: string[]
  upgrade?: boolean
}): Promise<QyViolationImportResult> {
  return qyPost<QyViolationImportResult>(
    '/admin/violation/rules/import-builtin',
    body
  )
}

export type QyViolationRecordListParams = {
  p: number
  page_size: number
  rule_id?: number
  /** 三态：不传 = 全部；`1` = 只看影子；`0` = 只看真实。 */
  shadow?: '0' | '1'
  shadow_reason?: string
  user_id?: number
  start_ts?: number
  end_ts?: number
}

/** 命中记录列表。这一页只用它做「按规则看影子命中」的分析，不做处置。 */
export function listQyViolationRecords(
  params: QyViolationRecordListParams
): Promise<QyPage<QyViolationRecord>> {
  return qyGet<QyPage<QyViolationRecord>>('/admin/violation/records', params)
}

/**
 * 导出命中记录为 CSV。
 *
 * 走鉴权接口 + Blob，而不是拼一个 `<a href>` 直链：这条路由要管理员身份，
 * 直链会因为缺 Bearer 直接 401，而浏览器下载失败时不会有任何可见提示。
 *
 * 不能用 `qyGet`：那条路会把响应体当 `{success,data}` 信封解，而这里的成功
 * 响应就是 CSV 文本本身。失败时 axios 给回的 `response.data` 也是 Blob
 * （responseType 对错误响应一视同仁），所以错误还原走 `qyErrorFromBlobFailure`。
 */
export async function exportQyViolationRecords(
  params: Omit<QyViolationRecordListParams, 'p' | 'page_size'>
): Promise<Blob> {
  try {
    const res = await api.get(
      `${QY_API_PREFIX}/admin/violation/records/export`,
      {
        skipErrorHandler: true,
        skipBusinessError: true,
        responseType: 'blob',
        params,
        // 上游的在途 GET 去重只按 url + params 归并，认不出 responseType 的差异。
        disableDuplicate: true,
      }
    )
    return res.data as Blob
  } catch (error) {
    throw await qyErrorFromBlobFailure(error)
  }
}

export function listQyViolationCounters(params: {
  p: number
  page_size: number
  user_id?: number
}): Promise<QyViolationCounterPage> {
  return qyGet<QyViolationCounterPage>('/admin/violation/counters', params)
}

/**
 * 把某个用户当前窗口的违规计数清零。
 *
 * 存在理由是历史脏数据：本轮之前影子命中也会推进 `hit_count`，现网的计数器
 * 因此被污染，而历史行无法分辨哪几次来自影子。后端只清 `hit_count` 与窗口起点，
 * `total_count`（终身累计）与 `ban_cycle`（封禁认领互斥键）都不动。
 */
export function resetQyViolationCounter(
  userId: number,
  reason: string
): Promise<{ reset: boolean; hit_count_before: number }> {
  return qyPost<{ reset: boolean; hit_count_before: number }>(
    `/admin/violation/counters/${userId}/reset`,
    { reason }
  )
}
