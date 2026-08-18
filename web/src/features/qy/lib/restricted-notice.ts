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
import { queryOptions, useQuery } from '@tanstack/react-query'

import { useQyIsRestricted } from './account-status'
import { qyGet, qyPut } from './api'
import { qyKeys } from './query-keys'

/**
 * 受限账号公告：管理员配的一段「你被限制了、该去哪申诉」。
 *
 * ## 三态
 *
 *   没配过 / 关闭 / 读取失败 → `enabled: false`，界面**回落到那条固定文案**
 *   已启用                  → `enabled: true` + 标题 + Markdown 正文
 *
 * 后端把「没配过」「关掉了」「扩展库不可用」全部收敛成同一个 `enabled: false`
 * （见 `qianye/controller/restricted_notice.go`），因为它们在展示上是同一件事：
 * 显示那条一直都在的固定说明。前端因此**不需要**也不应该区分它们 —— 区分只会
 * 长出三段各说各话的文案。
 *
 * ## 正文是 Markdown 源码，净化只发生在渲染那一步
 *
 * 后端一个字符都不改写（与工单正文同一口径）。渲染必须走
 * `components/ui/markdown` 的 `untrusted` 档 —— 与工单同一份显式白名单，
 * 而**不是**站点公告用的那份宽档。信任边界不同：站点公告是"管理员写、大家看"，
 * 这一段是"管理员写、**给正在申诉的用户看**"，而受限用户恰恰是最容易被一个
 * 假登录框骗走口令的人（他刚被封，正在找出口）。宽档放行 `<form>` / `<input>` /
 * 任意 `style`，那正好凑齐一个铺满全屏的假「验证身份以解除限制」表单。
 */
export type QyRestrictedNotice = {
  enabled: boolean
  title: string
  /** Markdown 源码。渲染时必须 `untrusted`。 */
  body: string
}

/** 读取失败与"没配过"共用的安全默认值。 */
export const QY_RESTRICTED_NOTICE_OFF: QyRestrictedNotice = {
  enabled: false,
  title: '',
  body: '',
}

function normalize(raw: unknown): QyRestrictedNotice {
  const data = (raw ?? {}) as Partial<QyRestrictedNotice>
  const title = typeof data.title === 'string' ? data.title.trim() : ''
  const body = typeof data.body === 'string' ? data.body.trim() : ''
  // 「开着但内容是空的」一律按关闭处理。后端已经挡过两道（写入校验 + 读取
  // normalize），这里是第三道，理由与后端那道相同：受限用户的首屏上出现一块
  // 空白卡片，比回落固定文案糟得多 —— 一个刚被封号的人会以为页面坏了。
  if (data.enabled !== true || title === '' || body === '') {
    return QY_RESTRICTED_NOTICE_OFF
  }
  return { enabled: true, title, body }
}

export function qyRestrictedNoticeQueryOptions() {
  return queryOptions({
    queryKey: qyKeys.restrictedNotice(),
    queryFn: async () => normalize(await qyGet('/restricted-notice')),
    // 失败不重试、不抛给页面：拿不到公告等于没配公告，界面照常显示固定文案。
    retry: false,
    staleTime: 60_000,
  })
}

/**
 * 读取当前应显示的公告。**只在受限账号上取数。**
 *
 * `enabled` 挂在 {@link useQyIsRestricted} 上是第二道闸：正常账号的浏览器
 * 连这次请求都不会发出去。第一道是调用点本身（横幅与落地页只在受限态渲染），
 * 第三道、也是真正的边界，在后端 —— handler 对非受限身份一律回空
 * （`middleware.IsRestrictedUser`）。三道里只有最后一道是安全边界，
 * 前两道是"别让正常用户的界面上凭空出现一句你被封了"。
 */
export function useQyRestrictedNotice(): QyRestrictedNotice {
  const restricted = useQyIsRestricted()
  const query = useQuery({
    ...qyRestrictedNoticeQueryOptions(),
    enabled: restricted,
  })
  // react-query 在 `enabled:false` 时仍会返回缓存里已有的数据，所以这里必须
  // 再判一次 restricted —— 否则一个刚被解除限制的用户在缓存过期之前还会看到
  // 那段公告。
  if (!restricted) return QY_RESTRICTED_NOTICE_OFF
  // 出口再 normalize 一次，而不是只信任 `queryFn` 那一次：缓存也可以被
  // `setQueryData` 从别处写入（预取、失效后的乐观写入、测试夹具）。normalize
  // 是幂等的，重跑一次的代价是两次 trim；漏跑一次的代价是受限用户首屏上
  // 一块空白卡片。
  return normalize(query.data)
}

// ───────────────────────────── 管理端 ─────────────────────────────

/** 管理端回读的完整档，含两条长度上限。 */
export type QyRestrictedNoticeAdmin = QyRestrictedNotice & {
  updated_at: number
  updated_by: number
  /** 上限随内容一起下发，前端不另写一份常量（写死那份迟早与后端漂移）。 */
  title_max_runes: number
  body_max_runes: number
}

export function qyAdminRestrictedNoticeQueryOptions() {
  return queryOptions({
    queryKey: qyKeys.adminRestrictedNotice(),
    queryFn: () => qyGet<QyRestrictedNoticeAdmin>('/admin/restricted-notice'),
    retry: false,
  })
}

export function putQyRestrictedNotice(payload: QyRestrictedNotice) {
  return qyPut<QyRestrictedNotice>('/admin/restricted-notice', payload)
}
