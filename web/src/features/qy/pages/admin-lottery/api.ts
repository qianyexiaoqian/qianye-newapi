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

import { qyDelete, qyGet, qyPost, qyPut } from '../../lib/api'
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
  QyLotAdminTextPrize,
  QyLotCoverUpload,
  QyLotCreateInput,
  QyLotPrizeSecret,
  QyLotSeries,
  QyLotSeriesInput,
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

/**
 * 上传一张封面，拿到可以填进活动的 `ref`。
 *
 * 两步式（先传图拿 ref、再保存活动）不是偷懒：保存活动那一步跑在一个要写四张表
 * 的事务里，把几 MiB 的上行塞进去等于让事务持有时间跟着管理员的带宽走。
 * 与工单图片同一条口径。
 */
export function uploadQyLotCover(file: File): Promise<QyLotCoverUpload> {
  const form = new FormData()
  form.append('file', file)
  return qyPost<QyLotCoverUpload>('/admin/lottery/covers', form)
}

/**
 * 丢弃一张**已上传但还没保存进任何活动**的封面。
 *
 * 没有它，管理员在向导里换一张图只会把本地那一项换掉，服务端那条未绑定的行
 * 会一直占着待用配额 —— 十次"选了又换"之后他在 24 小时宽限期到期之前再也
 * 传不了图，而收到的提示是"请先保存活动"，一个他此刻无法执行的动作。
 */
export function discardQyLotCover(ref: string): Promise<unknown> {
  return qyDelete<unknown>(`/admin/lottery/covers/${encodeURIComponent(ref)}`)
}

/**
 * 换掉一场活动的封面。两个字段都传空串 = 清空。
 *
 * **不限活动状态**，这是刻意的:封面不进 commit / rules / spec 三个哈希原像的
 * 任何一个，它不参与结果推导、也不是对用户的任何一项承诺 —— 而一个 404 的
 * 外链封面挂在正在进行的活动上时必须有办法修好。奖档、时刻、参与条件永远
 * 不在此列（它们只能在 `draft` 阶段改）。
 */
export function setQyLotActivityCover(
  actNo: string,
  body: { cover_url: string; cover_ref: string }
): Promise<{ cover_ref: string; cover_url: string }> {
  return qyPut<{ cover_ref: string; cover_url: string }>(
    actPath(actNo, '/cover'),
    body
  )
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
 * 下架：把一场**已结束**的活动从用户端的活动大厅撤下。
 *
 * 它与 {@link cancelQyLotActivity} 是两件事，弹窗上必须分得开：取消会全额退款
 * 并推动状态机，下架一分钱都不动、随时可以撤回。
 *
 * 下架**不遮**活动详情、我的参与与匿名证据链 —— 公正性一旦公布过就不能被运营
 * 收回，而参与过的人必须还能查到自己那一票。后端只允许对 `finished` 下架：
 * 下架一场进行中的活动等于一次隐蔽的提前截止。
 */
export function hideQyLotActivity(
  actNo: string,
  body: { reason: string }
): Promise<{ hidden_at: number }> {
  return qyPost<{ hidden_at: number }>(actPath(actNo, '/hide'), body)
}

/** 重新上架。下架本来就是可逆的，不给撤回入口等于把它变成不可逆。 */
export function unhideQyLotActivity(
  actNo: string
): Promise<{ hidden_at: number }> {
  return qyPost<{ hidden_at: number }>(actPath(actNo, '/unhide'))
}

/**
 * **彻底删除**一场活动。不可逆。
 *
 * 删掉的是这一场的全部行：活动、投注、事件、选项、派奖、奖品、种子、
 * 兑换码履历、对账异常。删完之后**审计行是唯一还能证明这一场存在过的东西**，
 * 而那一场的公正性从此无法被任何人验证（证据链端点会返回 404，仓库自带的
 * `lottery-verify.py` 再也拉不到数据）。
 *
 * 后端有六道硬闸门（未结束 / 出款未落定 / 文本奖未履行 / 参与未结算 /
 * 对账异常未处理 / 双色球系列结转未收口），任何一条不满足都会回 409 并附上
 * 精确的 `code`；`confirm_act_no` 必须与活动编号逐字相同 —— 二次确认在
 * **服务端**校验，只在前端弹一个"确定吗"挡不住脚本化的误调用。
 */
export function deleteQyLotActivity(
  actNo: string,
  body: { confirm_act_no: string; reason: string }
): Promise<unknown> {
  return qyDelete<unknown>(actPath(actNo), body)
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

// ───────────────────────── 文本奖履行 ─────────────────────────

/**
 * 文本奖履行队列。
 *
 * 端点挂在**模块下**（`/admin/lottery/text-prizes`）而不是活动下，`act_no` 只是
 * 它的一个过滤参数 —— 与对账异常（`/admin/lottery/flags`）同一条理由：待办红点
 * 要能一次看到全站还有多少份码没发，按活动过滤只是钻进去之后的事。
 */
export function qyAdminLotTextPrizesQuery(
  actNo: string,
  params: { fulfilled?: number; p: number; page_size: number }
) {
  return queryOptions({
    queryKey: qyKeys.adminLotteryTextPrizes(actNo, params),
    queryFn: () =>
      qyGet<QyPage<QyLotAdminTextPrize>>('/admin/lottery/text-prizes', {
        ...params,
        act_no: actNo === '' ? undefined : actNo,
      }),
    staleTime: 10_000,
  })
}

/**
 * 查看兑换码明文。**被审计的高敏操作**，与提现的收款信息同一条纪律：
 * 先填事由再请求，明文只活在组件内存里，不进任何缓存。
 *
 * 一键直出的话，管理员滑过列表时的随手点击会和真正的核对混在一起，
 * 事后的审计流水就失去了区分能力。
 */
export function revealQyLotPrizeSecret(body: {
  payout_no: string
  reason: string
}): Promise<QyLotPrizeSecret> {
  return qyPost<QyLotPrizeSecret>(
    `/admin/lottery/payouts/${encodeURIComponent(body.payout_no)}/reveal`,
    { reason: body.reason }
  )
}

/**
 * 履行：把实际的兑换码填进去，此后中奖者本人可以看到它。
 *
 * 后端用 CAS（`WHERE payout_no=? AND kind='text' AND fulfilled_at=0`），
 * 所以按钮连点第二次拿到的是"已履行过"而不是第二份记录 —— 天然幂等。
 */
export function fulfillQyLotPrize(body: {
  note: string
  payout_no: string
  secret: string
}): Promise<unknown> {
  return qyPost<unknown>(
    `/admin/lottery/payouts/${encodeURIComponent(body.payout_no)}/fulfill`,
    { secret: body.secret, note: body.note }
  )
}

/**
 * 撤销履行标记（账面纠错）。
 *
 * **不清空已存的兑换码**：用户可能已经看到并用掉了那个码，抹掉记录等于抹掉
 * 争议时唯一的证据。撤销只是把"已履行"这个账面结论收回来。
 *
 * 只对 `kind='text'` 开放 —— 额度奖的 `paid` 是资金终态，永远不可撤。
 */
export function unfulfillQyLotPrize(body: {
  payout_no: string
  reason: string
}): Promise<unknown> {
  return qyPost<unknown>(
    `/admin/lottery/payouts/${encodeURIComponent(body.payout_no)}/unfulfill`,
    { reason: body.reason }
  )
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

// ───────────────────────── 双色球期次系列 ─────────────────────────

/**
 * 系列列表。
 *
 * 端点挂在模块下（`/admin/lottery/series`）而不是活动下：一个系列跨很多期，
 * 而"这个系列还能再注多少钱"（`headroom_quota`）是运营在开新一期之前必须先
 * 看到的数，与任何一期都无关。
 */
export function qyAdminLotSeriesQuery(params: {
  p: number
  page_size: number
}) {
  return queryOptions({
    queryKey: qyKeys.adminLotterySeries(params),
    queryFn: () => qyGet<QyPage<QyLotSeries>>('/admin/lottery/series', params),
    staleTime: 15_000,
  })
}

/**
 * 创建一个系列。
 *
 * `issue_cap_quota` 在这一刻**冻结**，此后没有任何接口能改它 —— 它是整个系列
 * （无论开多少期）累计净增发的上界。号池四元组同理：期与期之间不可变，
 * 因为可变的号码空间等于可变的中奖概率。
 */
export function createQyLotSeries(
  body: QyLotSeriesInput
): Promise<QyLotSeries> {
  return qyPost<QyLotSeries>('/admin/lottery/series', body)
}

/**
 * 给系列注资。**平台唯一一处主动扩大发行上限的动作**，写审计。
 *
 * 后端把封顶判定与写入放在同一条条件 UPDATE 里
 * （`WHERE seed_total_quota + ? <= issue_cap_quota`），所以两个管理员同时注资
 * 不会各自读到旧值再各自通过校验。
 */
export function fundQyLotSeries(
  seriesNo: string,
  body: { amount: number; note: string }
): Promise<QyLotSeries> {
  return qyPost<QyLotSeries>(
    `/admin/lottery/series/${encodeURIComponent(seriesNo)}/fund`,
    body
  )
}

/**
 * 关闭系列。**滚存余额作废**，所以必须填原因并对参与者公示。
 *
 * 它从来不是任何用户的钱：每一期的投注在当期就已完成再分配（入池部分归池子、
 * 其余是平台手续费），滚存只是平台尚未发出去的发行额度。但用户不会自己想到
 * 这一点，所以规则页要事前写清。
 */
export function closeQyLotSeries(
  seriesNo: string,
  body: { reason: string }
): Promise<unknown> {
  return qyPost<unknown>(
    `/admin/lottery/series/${encodeURIComponent(seriesNo)}/close`,
    body
  )
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
