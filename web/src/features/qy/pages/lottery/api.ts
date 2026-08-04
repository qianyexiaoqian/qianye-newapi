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
  QyLotEntryReceipt,
  QyLotMyEntry,
  QyLotProof,
} from './types'

/** 大厅一页的条数。卡片比表格行高，一屏放不下更多。 */
export const QY_LOT_PAGE_SIZE = 12

export type QyLotActivityListParams = {
  p: number
  page_size: number
  kind?: string
  /** `open` = 进行中（published/locked/settling），`done` = 已结束。 */
  status?: string
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
  pay_password?: string
}

/**
 * 报名 / 投注。
 *
 * 返回的是**报名回执**：`entry_no` + `chain_hash` + `seq`。用户手里的这份副本
 * 是平台自己签发的，事后动名单必须同时改掉 N 个用户已经看到过的值 ——
 * 所以前端必须把它落到"我的参与记录"里长期可见，而不是弹一个 toast 就没了。
 */
export function submitQyLotEntry(
  actNo: string,
  body: QyLotEntryInput
): Promise<QyLotEntryReceipt> {
  return qyPost<QyLotEntryReceipt>(
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
