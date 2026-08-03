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
import { TabsContent } from '@/components/ui/tabs'

import { useQyConfig } from '../../hooks/use-qy-config'
import {
  QY_WALLET_TRANSFER_TAB,
  qyWalletTransferVisible,
} from '../../wallet-entry/tab'
import { QyPageTabs } from '../components/qy-page-tabs'
import { QyPayPasswordBody } from '../pay-password'
import { QyTransferBody } from '../transfer'
import { QyTransferLogsBody } from '../transfer-logs'

/**
 * 钱包页「余额划转」标签的面板（需求 2 + 需求 5）。
 *
 * ── 与上一轮的差别 ──
 * 上一轮这里是钱包页**底部**另起的一块 `<section>`，自带一行小标题「余额划转」。
 * 项目方原话：「余额划转这个 tab 你和 添加资金/订阅套餐 这样放到一起，不要在
 * 底部添加，丑死了。」所以整块搬进上游那组 Tabs 里，成为第三个面板：
 *   · 小标题删掉 —— 标签本身就写着「余额划转」，再来一行是同一个词说两遍；
 *   · 面板里仍是原来那三张标签（发起划转 / 划转记录 / 支付密码），
 *     顺序与可见性照旧由 `QY_TAB_GROUPS` × `isQyPageVisible` 决定。
 *
 * ── 挂载位置 ──
 * 必须渲染在上游 `<Tabs>` 的**内部**（Base UI 的 `Tabs.Panel` 靠 context 找根）。
 * 上游 `features/wallet/index.tsx` 里因此多了一行 `<QyWalletTransferPanel />`，
 * 与触发器 `<QyWalletTransferTrigger />` 一起，是 qy 在钱包页 Tabs 上的全部占用。
 * `wallet-entry/__tests__/wallet-tab.test.ts` 用 AST 断言这两行确实分别落在
 * `<Tabs>` 与 `<TabsList>` 里面 —— "组件写了但挂在插槽外从没被渲染" 是本仓
 * 反复出现的形状。
 *
 * ── 为什么面板自己也判一次可见性 ──
 * 触发器不渲染时，面板留着并不会显示（没有触发器就选不中它），但 Base UI 的
 * `Tabs.Panel` 仍会在树里存在。更要紧的是里面那三个 body 各自带查询，
 * 划转功能关掉时不该有任何一个被挂起来。判定共用 `qyWalletTransferVisible`，
 * 与触发器同一处实现。
 */
export function QyWalletTransferPanel() {
  const config = useQyConfig()

  if (!qyWalletTransferVisible(config)) return null

  return (
    <TabsContent value={QY_WALLET_TRANSFER_TAB}>
      <QyPageTabs
        host='/wallet'
        bodies={{
          '/qy/transfer': <QyTransferBody />,
          '/qy/transfer-logs': <QyTransferLogsBody />,
          '/qy/pay-password': <QyPayPasswordBody />,
        }}
      />
    </TabsContent>
  )
}
