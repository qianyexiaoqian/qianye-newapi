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
import { useLocation, useNavigate } from '@tanstack/react-router'
import { useCallback } from 'react'

import { useQyConfig } from '../hooks/use-qy-config'
import { QY_TAB_GROUPS, qyTabHash } from '../lib/pages'
import type { QyConfig } from '../lib/types'

/**
 * 钱包页顶部那一排标签里，「余额划转」这一格的取值（需求 5）。
 *
 * 项目方原话：「余额划转这个 tab 你和 添加资金/订阅套餐 这样放到一起，不要在
 * 底部添加，丑死了。」
 *
 * ── 为什么是**一格**而不是三格 ──
 * 上一轮做的是「发起划转 / 划转记录 / 支付密码」三张标签。三张平铺进上游那排
 * 会得到 5 格，问题有两个：
 *   · **层级不齐**。上游那两格回答的是"我怎么把额度弄进来"（充值渠道 / 套餐），
 *     而三张划转标签是同一件事的三个步骤，其中「支付密码」还是个账户安全设置。
 *     把它和「添加资金」摆成同级，等于告诉用户这是第五种充值方式。
 *   · **窄屏排不下**。上游那排在 `sm` 以下是 `grid grid-cols-2`，5 格要折成 3 行，
 *     比现在还难看，而"丑死了"正是这次要修的。
 * 所以顶层只加一格，点进去里面仍是原来那三张标签（`QyPageTabs`）。
 *
 * ── 为什么取值不进 `?tab=` ──
 * 上游钱包路由的 `validateSearch` 是 `z.enum(WALLET_TAB_VALUES).catch(...)`：
 * 未登记的取值不是被忽略，而是被**改写回 `funds`**，标签会立刻自己弹回去。
 * 要登记就得改上游的 `constants.ts` 与路由 schema 两个文件。而 qy 早就有一套
 * 不经过 `validateSearch` 的状态载体 —— 划转那三张标签用的 URL hash
 * （见 `pages/lib/tabs.ts`）。这里复用它：hash 落在这三张里的任意一张，
 * 就说明用户正待在「余额划转」这一格。一个状态、一处实现，不再多一套。
 */
export const QY_WALLET_TRANSFER_TAB = 'qy-transfer'

/**
 * 「余额划转」格子里那三张标签各自的 hash。
 *
 * 从 `QY_TAB_GROUPS` 现算而不是抄一份字面量：抄一份的话，往选择夹里加一张
 * 标签之后，顶层这一格认不出新标签的 hash，用户带着深链进来会落在「添加资金」上。
 */
const WALLET_TAB_HASHES: readonly string[] = (
  QY_TAB_GROUPS.find((group) => group.host === '/wallet')?.pages ?? []
).map(qyTabHash)

/**
 * 这一格该不该出现。
 *
 * 与划转板块此前的判定逐字相同（功能开关 × 钱包入口开关），但**只此一处**：
 * 触发器与面板都调它，否则会出现"标签在、点进去是空的"或反过来。
 * 不看 `available`：扩展库降级时格子仍在，由里面的标签各自显示"暂不可用"，
 * 比入口凭空消失更好排查（热路径 fail-open）。
 */
export function qyWalletTransferVisible(config: QyConfig): boolean {
  return (
    config.enabled &&
    config.features.transfer &&
    config.wallet.show_transfer_entry
  )
}

/**
 * 顶层「余额划转」格的选中状态与切换动作。
 *
 * `replace: true` 与上游钱包页切 tab 的做法一致：返回键的预期是"离开钱包页"，
 * 而不是"撤销一次标签切换"。
 */
export function useQyWalletTransferTab() {
  const config = useQyConfig()
  const hash = useLocation({ select: (location) => location.hash })
  const navigate = useNavigate()

  const visible = qyWalletTransferVisible(config)
  const current = (hash ?? '').replace(/^#/, '')
  const active = visible && WALLET_TAB_HASHES.includes(current)

  const activate = useCallback(() => {
    const first = WALLET_TAB_HASHES[0]
    if (first == null) return
    void navigate({ to: '.', hash: first, replace: true })
  }, [navigate])

  // 切回「添加资金 / 订阅套餐」时要把 hash 摘掉，否则下次刷新又会被认成
  // 「余额划转」。只在 hash 确实是划转那几张时才导航：别人（上游锚点、
  // 其它扩展）留下的 hash 不该被这里顺手清掉。
  const clear = useCallback(() => {
    if (!WALLET_TAB_HASHES.includes(current)) return
    void navigate({ to: '.', hash: '', replace: true })
  }, [navigate, current])

  return { visible, active, activate, clear }
}
