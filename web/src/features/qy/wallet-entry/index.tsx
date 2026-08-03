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
import { Repeat } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { TabsTrigger } from '@/components/ui/tabs'

import { QY_WALLET_TRANSFER_TAB, useQyWalletTransferTab } from './tab'

/**
 * qy 在上游钱包页上的**全部**落点。
 *
 * ── 这里曾经还有一张「增长与结算」入口卡 ──
 * 它把推广佣金 / 提现两个链接摆在钱包页底部（`QyWalletEntryCard` /
 * `QyWalletSections`）。项目方要求钱包页瘦身，那张卡整体删除，不是挪位置：
 * 两个链接在侧栏里本来就各有一行入口，卡片只是同一入口的第二份拷贝，
 * 留着迟早出现"侧栏改了名、卡片没改"的漂移。
 *
 * 同一轮里上游那张「推荐计划」卡（`features/wallet/components/affiliate-rewards-card`）
 * 也从钱包页撤下，改由「推广佣金」页复用同一个组件渲染 —— 见
 * `pages/affiliate/components/referral-program-card.tsx`。
 *
 * 于是钱包页上现在只剩「余额划转」那一格：一个触发器 + 一个面板。
 */

/**
 * 钱包页上游那排标签里，「余额划转」那一格的触发器（需求 5）。
 *
 * 只渲染一个 `TabsTrigger`，**必须放进上游的 `<TabsList>` 里面** —— Base UI
 * 的 `Tabs.Tab` 靠 list 的 context 拿索引与键盘导航，放在外面会得到一个
 * 按不动、Tab 键也走不到的按钮。这一条由
 * `__tests__/wallet-tab.test.ts` 的 AST 断言钉住。
 *
 * 图标用 `Repeat`（与侧栏「划转流水」同一个），与上游那两格的 `size-3.5`
 * 对齐；不给它 `aria-hidden` 之外的语义，标签文字本身就是标签名。
 */
export function QyWalletTransferTrigger() {
  const { t } = useTranslation()
  const { visible } = useQyWalletTransferTab()

  if (!visible) return null

  return (
    <TabsTrigger value={QY_WALLET_TRANSFER_TAB} className='gap-1.5 px-3'>
      <Repeat className='size-3.5' aria-hidden='true' />
      {t('qy_nav_transfer')}
    </TabsTrigger>
  )
}

// 只再导出组件。取值常量与 hook 留在 `./tab`，由钱包页直接从那里 import：
// 组件文件里混着导出非组件会让 Vite 的 fast refresh 整个文件失效
// （oxlint 的 react/only-export-components 就是在盯这个）。
export { QyWalletTransferPanel } from '../pages/wallet-transfer'
