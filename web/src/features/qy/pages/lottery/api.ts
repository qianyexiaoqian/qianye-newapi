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

import { qyGet, qyPost } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyPage } from '../../lib/types'
import type {
  QyLotActivityBrief,
  QyLotActivityDetail,
  QyLotEligibility,
  QyLotEntryBatch,
  QyLotMyEntry,
  QyLotMyPrize,
  QyLotProof,
  QyLotSeriesView,
} from './types'

/** 大厅一页的条数。卡片比表格行高，一屏放不下更多。 */
export const QY_LOT_PAGE_SIZE = 12

/**
 * 大厅分区。**参数名与取值必须与后端 `hallPhases` 逐字一致**。
 *
 * 上一版这里叫 `status`、取值是 `open|done`，而后端读的是 `phase`、只认
 * `live|ended`，那个 switch 又没有 default 分支 —— 于是两张标签发出两个不同的
 * URL、拿回同一份列表（全部非草稿），全链路没有任何一处报错。库里当时
 * published/locked/settling 一条都没有，用户点开「进行中」看到的是 64 条已经
 * 结束的活动，这就是项目方反馈的"已结束和进行中没有进行区分"。
 *
 * 现在后端对未登记的取值一律 400（`qy_lot_bad_phase`），再漂移一次会当场炸，
 * 不会再安静地退回"两张标签一模一样"。
 */
export type QyLotHallPhase = 'ended' | 'live'

/**
 * 大厅的三张选择夹。**参数名与取值必须与后端 `hallLanes` 逐字一致**。
 *
 * ── 为什么参数名是 `lane` 而不是 `kind` ──
 * 取值与 `kind` 长得一样（`draw` / `guess`），语义却不同：`lane='draw'` 恰好
 * **排除**双色球，而 `kind='draw'` 包含它。两个同名不同义的参数并存，迟早有人
 * 按 `kind` 的直觉去读 `lane` 的值，而那次误读在界面上的表现是「双色球标签里
 * 长出了普通抽奖」—— 接口 200、列表非空、没有任何一处报错。
 *
 * 后端对未登记的取值一律 400（`qy_lot_bad_lane`），与 `phase` 同一条纪律。
 */
export type QyLotHallLane = 'ball' | 'draw' | 'guess'

export type QyLotActivityListParams = {
  p: number
  page_size: number
  /** `draw` = 按名次/按公示概率，`ball` = 双色球，`guess` = 竞猜。 */
  lane?: QyLotHallLane
  /** `live` = 进行中（published/locked/settling），`ended` = 已结束。 */
  phase?: QyLotHallPhase
}

export function qyLotActivitiesQuery(params: QyLotActivityListParams) {
  return queryOptions({
    queryKey: qyKeys.lotteryActivities(params),
    queryFn: () =>
      qyGet<QyPage<QyLotActivityBrief>>('/lottery/activities', params),
    staleTime: 15_000,
  })
}

export function qyLotActivityQuery(actNo: string) {
  return queryOptions({
    queryKey: qyKeys.lotteryActivity(actNo),
    queryFn: () =>
      qyGet<QyLotActivityDetail>(
        `/lottery/activities/${encodeURIComponent(actNo)}`
      ),
    // 倒计时与盘口在开放期一直在变，10 秒是"不至于误导"与"不刷爆接口"的折中。
    staleTime: 10_000,
    enabled: actNo !== '',
  })
}

/**
 * 双色球期次系列。
 *
 * 详情页只从这一份里取一件事：**下一期什么时候**。那是一个买过彩票的人一定会
 * 问的问题，而它按定义不在"本期"的活动记录里 —— 下一期是系列上另一场活动。
 *
 * 后端刻意不下发任何概率数字（见 `lib/ball.ts` 顶部），这里也不例外。
 */
export function qyLotSeriesQuery(seriesNo: string, enabled: boolean) {
  return queryOptions({
    queryKey: qyKeys.lotterySeries(seriesNo),
    queryFn: () =>
      qyGet<QyLotSeriesView>(`/lottery/series/${encodeURIComponent(seriesNo)}`),
    staleTime: 30_000,
    enabled: enabled && seriesNo !== '',
  })
}

/**
 * 资格预检。
 *
 * **只用于展示**——把"我为什么不能参加"摊开给用户看，替代一个冷冰冰的置灰按钮。
 * 它绝不是放行依据：真正说了算的是报名接口在活动行锁与主库行锁里的那两次判定，
 * 预检与它之间隔着一整段时间，用户的余额、分组、封禁状态都可能变。
 */
export function qyLotEligibilityQuery(actNo: string, enabled: boolean) {
  return queryOptions({
    queryKey: qyKeys.lotteryEligibility(actNo),
    queryFn: () =>
      qyGet<QyLotEligibility>(
        `/lottery/activities/${encodeURIComponent(actNo)}/eligibility`
      ),
    staleTime: 30_000,
    enabled: enabled && actNo !== '',
  })
}

export type QyLotEntryInput = {
  /** 每次**点击**生成一次，重试沿用 —— 这是幂等键的唯一来源。 */
  client_request_id: string
  /** 抽奖恒为 0；竞猜是所选选项的稳定编号。 */
  opt_no: number
  /**
   * 双色球选号 `03,05,12|02`。**非双色球必须不带**：后端对带号的普通抽奖是
   * 拒绝而不是忽略（静默忽略会让一个填了号码的请求照常成功，用户以为自己买的
   * 是那组号）。
   *
   * 机选与自选在协议上完全等价：机选只是本地的 `crypto.getRandomValues`，
   * 服务端不区分——号码一旦进链两者的可验证性一模一样。
   */
  pick?: string
  /**
   * 一次买多注的选号列表（双色球）。每一项与 `pick` 同格式。
   *
   * N 注 = N × 单注参与费，每一注各自进哈希链、各自与开奖号比对、各自定档 ——
   * 与用户连点 N 次完全同构，区别只在于这 N 次在服务端串行跑完、总额在按下
   * 确认之前就已经写在屏幕上。上限由详情页的 `max_picks_per_request` 下发。
   *
   * **与 `pick` 互斥**：两者同时非空时后端 400（`qy_lot_pick_conflict`），
   * 不做静默择一 —— 择一意味着有一半的请求买到的不是它写的那组号。
   *
   * 允许重号：同一次提交里两注号码完全相同是两张独立的票，中奖时各拿一份。
   */
  picks?: string[]
  pay_password?: string
}

/**
 * 报名 / 投注。
 *
 * 返回的是**回执批**：单注与多注同一个形状。这条链路上唯一会不一致的两个数
 * 就是"我发了几注"与"服务端收下了几注"（余额不足、撞上每人上限、时间预算用完
 * 都会让后半批停下），一个恒定的形状让 `accepted` / `total_quota` 必须被读出来，
 * 而不是被假设。
 *
 * 每一份回执 `entry_no` + `chain_hash` + `seq` 都是平台自己签发的副本，事后动
 * 名单必须同时改掉 N 个用户已经看到过的值 —— 所以前端必须把它落到"我的参与
 * 记录"里长期可见，而不是弹一个 toast 就没了。
 */
export function submitQyLotEntry(
  actNo: string,
  body: QyLotEntryInput
): Promise<QyLotEntryBatch> {
  return qyPost<QyLotEntryBatch>(
    `/lottery/activities/${encodeURIComponent(actNo)}/entries`,
    body
  )
}

export function qyLotMyEntriesQuery(params: { p: number; page_size: number }) {
  return queryOptions({
    queryKey: qyKeys.lotteryMyEntries(params),
    queryFn: () => qyGet<QyPage<QyLotMyEntry>>('/lottery/my-entries', params),
    staleTime: 15_000,
  })
}

/**
 * 我中的那一份文本奖（兑换码 / CDK / 实物说明）。
 *
 * ## 为什么是逐条拉取
 *
 * 一个返回全部正文的列表接口，意味着**一次越权 bug 就是全量泄漏**。这里只按
 * `payout_no` 逐条取，而 `payout_no` 由服务端 crypto/rand 生成、不可枚举，
 * 所以这个限制没有可用性代价。后端还会再校验一次 `user_id` 与 `kind='text'`。
 *
 * ## 为什么不缓存
 *
 * `staleTime: 0` + 不写长缓存：兑换码不该在用户切走之后还留在内存里被别的
 * 页面读到。这与提现的收款信息明文是同一条纪律。
 */
export function qyLotMyPrizeQuery(payoutNo: string, enabled: boolean) {
  return queryOptions({
    queryKey: qyKeys.lotteryMyPrize(payoutNo),
    queryFn: () =>
      qyGet<QyLotMyPrize>(`/lottery/my/prizes/${encodeURIComponent(payoutNo)}`),
    staleTime: 0,
    gcTime: 0,
    enabled: enabled && payoutNo !== '',
  })
}

/**
 * 证据链。
 *
 * 走 `/lottery/public/:act_no/proof`，**匿名可访问**（站点可在配置里关掉）。
 * 需要登录才能验证的公正性不叫公正性，所以这个 URL 是可以直接发给任何人的。
 *
 * `page_size` 默认要够大：`lib/verify.ts` 拒绝在只拿到一页时验证链与名单——
 * 少了任何一条，链就断了，而"断了"与"被篡改了"在验证结果上无法区分。
 */
export function qyLotProofQuery(
  actNo: string,
  params: { p: number; page_size: number },
  enabled: boolean
) {
  return queryOptions({
    queryKey: qyKeys.lotteryProof(actNo, params),
    queryFn: () =>
      qyGet<QyLotProof>(
        `/lottery/public/${encodeURIComponent(actNo)}/proof`,
        params
      ),
    // 揭示之后证据链就不再变了，缓存久一点没有任何风险。
    staleTime: 60_000,
    enabled: enabled && actNo !== '',
  })
}

/**
 * 证据链的完整下载地址（NDJSON）。
 *
 * 第一行是文档头（承诺、名单哈希、种子、结果），之后每行一条参与记录 ——
 * 这一份是**自洽**的，可以直接喂给仓库里的 `qianye/docs/lottery-verify.py`。
 */
export function qyLotProofDownloadUrl(actNo: string): string {
  return `/api/qy/lottery/public/${encodeURIComponent(actNo)}/proof?format=ndjson`
}

/** 服务端单页条目上限。超过 1000 的请求会被**回落成默认页长**，不是夹到上限。 */
const PROOF_MAX_PAGE_SIZE = 1000

/**
 * 取回**整份**证据链（自动翻页）。
 *
 * 单页拿不完就验不了链与名单：少一条链就断，而"断了"与"被篡改了"在验证结果上
 * 无法区分。而且页长不能靠"要一个很大的数"来解决 —— 服务端的分页口径是
 * 越界即**回落默认值**（200），要得越多反而拿得越少，>200 人的活动会永远
 * 停在"数据不完整、已跳过"。所以这里老老实实按上限翻页拼起来。
 *
 * 逐页请求之间活动已经封盘、条目不会再变，所以拼接是安全的；万一 total 在
 * 翻页途中变了（只可能是有人在改库），拼出来的那一份会在链校验那一步露出来。
 */
export function qyLotFullProofQuery(actNo: string, enabled: boolean) {
  return queryOptions({
    queryKey: qyKeys.lotteryProof(actNo, { full: 1 }),
    queryFn: async (): Promise<QyLotProof> => {
      const base = `/lottery/public/${encodeURIComponent(actNo)}/proof`
      const first = await qyGet<QyLotProof>(base, {
        p: 1,
        page_size: PROOF_MAX_PAGE_SIZE,
      })
      const entries = [...first.entries]
      // 上界防的是"服务端 total 与实际条目对不上"时的死循环：翻到没有新数据
      // 就停,拿到多少算多少,由 verify 那一步如实标"不完整"。
      for (
        let page = 2;
        entries.length < first.total && page <= 200;
        page += 1
      ) {
        const next = await qyGet<QyLotProof>(base, {
          p: page,
          page_size: PROOF_MAX_PAGE_SIZE,
        })
        if (next.entries.length === 0) break
        entries.push(...next.entries)
      }
      return { ...first, entries }
    },
    // 揭示之后证据链就不再变了，缓存久一点没有任何风险。
    staleTime: 60_000,
    enabled: enabled && actNo !== '',
  })
}
