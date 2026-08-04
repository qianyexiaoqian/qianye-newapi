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
import { queryOptions } from '@tanstack/react-query'

import { qyGet, qyPost, qyPut } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyPage } from '../../lib/types'
import type {
  QyLotAdminActivityBrief,
  QyLotAdminActivityView,
  QyLotAdminConfig,
  QyLotAdminEntry,
  QyLotAdminEvent,
  QyLotAdminFlag,
  QyLotAdminPayout,
  QyLotCreateInput,
} from './types'

const BASE = '/admin/lottery/activities'

function actPath(actNo: string, suffix = ''): string {
  return `${BASE}/${encodeURIComponent(actNo)}${suffix}`
}

export type QyLotAdminListParams = {
  p: number
  page_size: number
  kind?: string
  status?: string
}

export function qyAdminLotActivitiesQuery(params: QyLotAdminListParams) {
  return queryOptions({
    queryKey: qyKeys.adminLotteryActivities(params),
    queryFn: () => qyGet<QyPage<QyLotAdminActivityBrief>>(BASE, params),
    staleTime: 15_000,
  })
}

/**
 * 活动详情。返回的是 `{activity, prizes, options, economics}` 信封 ——
 * 奖档与选项是独立的表，它们的生命周期与活动行不同（草稿期整体替换，
 * 发布之后连同 `spec_hash` 一起冻结），所以独立下发而不是嵌进活动行。
 */
export function qyAdminLotActivityQuery(actNo: string) {
  return queryOptions({
    queryKey: qyKeys.adminLotteryActivity(actNo),
    queryFn: () => qyGet<QyLotAdminActivityView>(actPath(actNo)),
    staleTime: 10_000,
    enabled: actNo !== '',
  })
}

/** 创建。落地即 `draft`，种子在这一刻由服务端生成 —— 请求体里没有它。 */
export function createQyLotActivity(
  body: QyLotCreateInput
): Promise<{ act_no: string }> {
  return qyPost<{ act_no: string }>(BASE, body)
}

/**
 * 发布。**不可逆**：这一刻算出三个哈希写进活动行，此后条件 / 奖档 / 选项 /
 * 四个时刻 / 费率全部只读。
 *
 * 后端用条件 UPDATE（`WHERE status='draft'`）保证只会成功一次，所以按钮连点
 * 第二次会拿到 409 而不是第二份承诺。
 */
export function publishQyLotActivity(
  actNo: string
): Promise<{ commit_hash: string }> {
  return qyPost<{ commit_hash: string }>(actPath(actNo, '/publish'))
}

/**
 * 取消整场。必然全额退款、必然公示、必然写审计。
 *
 * 这是管理员在开奖这件事上**唯一**能做的动作：他只能「不开」，不能「挑一个开」。
 */
export function cancelQyLotActivity(
  actNo: string,
  body: { reason: string }
): Promise<unknown> {
  return qyPost<unknown>(actPath(actNo, '/cancel'), body)
}

/**
 * 录入竞猜结果。**一经写入不可修改**（后端 CAS `WHERE win_option_id=0`）。
 *
 * 录错只能整场作废 + 全额退款 + 公示 —— 这把作弊面从"悄悄地反复调整"压缩到
 * "一次性地公开撒谎"。`evidence` 是强制的，且对用户公开。
 */
export function setQyLotGuessResult(
  actNo: string,
  body: { opt_no: number; evidence: string }
): Promise<unknown> {
  return qyPost<unknown>(actPath(actNo, '/guess-result'), body)
}

/**
 * 重试一笔卡住的出款。
 *
 * 它只把 `next_attempt_at` 置零并把 `held` 推回 `planned`，**绝不新建 payout
 * 行** —— 幂等键就是 `payout_no`，新建一行等于给同一张票发第二次钱。
 */
export function retryQyLotPayout(
  actNo: string,
  payoutNo: string
): Promise<unknown> {
  return qyPost<unknown>(
    `${actPath(actNo, '/payouts')}/${encodeURIComponent(payoutNo)}/retry`
  )
}

export function qyAdminLotEntriesQuery(
  actNo: string,
  params: { p: number; page_size: number; status?: string; user_id?: number }
) {
  return queryOptions({
    queryKey: qyKeys.adminLotteryEntries(actNo, params),
    queryFn: () =>
      qyGet<QyPage<QyLotAdminEntry>>(actPath(actNo, '/entries'), params),
    staleTime: 15_000,
    enabled: actNo !== '',
  })
}

export function qyAdminLotPayoutsQuery(
  actNo: string,
  params: { p: number; page_size: number; status?: string }
) {
  return queryOptions({
    queryKey: qyKeys.adminLotteryPayouts(actNo, params),
    queryFn: () =>
      qyGet<QyPage<QyLotAdminPayout>>(actPath(actNo, '/payouts'), params),
    staleTime: 10_000,
    enabled: actNo !== '',
  })
}

export function qyAdminLotEventsQuery(actNo: string) {
  return queryOptions({
    queryKey: qyKeys.adminLotteryEvents(actNo),
    queryFn: () => qyGet<QyPage<QyLotAdminEvent>>(actPath(actNo, '/events')),
    staleTime: 15_000,
    enabled: actNo !== '',
  })
}

/**
 * 一场活动的对账异常。
 *
 * 异常表挂在模块下而不是活动下（`/admin/lottery/flags?act_no=`）：红点列表要能
 * 一次看到**全站**未解决的异常，按活动过滤只是它的一个参数。
 */
export function qyAdminLotFlagsQuery(actNo: string) {
  return queryOptions({
    queryKey: qyKeys.adminLotteryFlags(actNo),
    queryFn: () =>
      qyGet<QyPage<QyLotAdminFlag>>('/admin/lottery/flags', {
        act_no: actNo,
        p: 1,
        page_size: 100,
      }),
    staleTime: 15_000,
    enabled: actNo !== '',
  })
}

export function qyAdminLotConfigQuery() {
  return queryOptions({
    queryKey: qyKeys.adminLotteryConfig(),
    queryFn: () => qyGet<QyLotAdminConfig>('/admin/lottery/config'),
  })
}

/**
 * 修改配置。
 *
 * 请求体是稀疏 map，**只传改动过的键** —— 后端逐键写 `qy_settings` 并写一条
 * 审计，把没改的键一起发过去会污染「谁在什么时候把奖品上限从 500 万改成
 * 5000 万」的追溯轨迹。
 */
export function updateQyLotConfig(
  patch: Record<string, string>
): Promise<unknown> {
  return qyPut<unknown>('/admin/lottery/config', patch)
}
