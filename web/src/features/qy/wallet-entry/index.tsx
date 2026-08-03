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
import { Link, type LinkProps } from '@tanstack/react-router'
import { ArrowRight, Banknote, Megaphone, Repeat } from 'lucide-react'
import type { ComponentType } from 'react'
import { useTranslation } from 'react-i18next'

import { Card, CardContent } from '@/components/ui/card'
import { IconBadge, type IconBadgeTone } from '@/components/ui/icon-badge'
import { TabsTrigger } from '@/components/ui/tabs'
import { cn } from '@/lib/utils'

import { useQyConfig } from '../hooks/use-qy-config'
import { qyTabTarget } from '../lib/pages'
import type { QyConfig } from '../lib/types'
import { QY_WALLET_TRANSFER_TAB, useQyWalletTransferTab } from './tab'

/**
 * 钱包页的扩展功能入口卡。
 *
 * 裁定文档 C33：划转 / 推广 / 提现三个模块原本各要往钱包页插一张卡，会在同一
 * 处反复抢行。这里合并成唯一一张卡，钱包页只承担 1 个 import + 1 行 JSX。
 *
 * 卡片刻意放在 Tabs **之外**：它既不属于"添加资金"也不属于"订阅套餐"，
 * 而且塞进任一 Tab 都会让另一个 Tab 的用户找不到入口。
 *
 * ── 需求 2 / 5 之后：划转不再是一个"入口"──
 * 余额划转已经是钱包页顶部那排标签里的一格（`QyWalletTransferPanel`），
 * 所以入口卡里那条指向 `/qy/transfer` 的链接删掉了：同一屏上放一个"点了之后
 * 跳到本屏另一张标签"的链接，只会让人以为还有别的页面。
 */

type QyWalletEntry = {
  key: string
  titleKey: string
  descKey: string
  // 上游 NavItem 用的就是这个宽松写法：既保留已存在路由的自动补全，
  // 又允许指向本次尚未落地、由其他页面代理并行开发的 qy 子路由。
  to: LinkProps['to'] | (string & {})
  /**
   * 直接落到宿主页的某一张标签。
   *
   * 提现现在是「推广佣金」选择夹里的一格，指向 `/qy/withdraw` 虽然也能到
   * （旧路由会重定向），但那是多跳一次的写法。宿主与 hash 都由
   * `qyTabTarget` 从 `QY_TAB_GROUPS` 现算，把提现搬去别的宿主时这里自动跟着走。
   */
  hash?: string
  icon: ComponentType<{ className?: string }>
  tone: IconBadgeTone
  /** 该入口依赖的功能开关 + 钱包展示开关，两者都为真才渲染。 */
  visible: (config: QyConfig) => boolean
}

const ENTRIES: QyWalletEntry[] = [
  {
    key: 'commission',
    titleKey: 'qy_nav_commission_hub',
    descKey: 'qy_common_wallet_entry_commission_desc',
    to: '/qy/affiliate',
    icon: Megaphone,
    tone: 'chart-3',
    visible: (config) =>
      config.features.commission && config.wallet.show_commission_entry,
  },
  {
    key: 'withdraw',
    titleKey: 'qy_nav_withdraw',
    descKey: 'qy_common_wallet_entry_withdraw_desc',
    ...qyTabTarget('/qy/withdraw'),
    icon: Banknote,
    tone: 'chart-4',
    visible: (config) =>
      config.features.withdraw && config.wallet.show_withdraw_entry,
  },
]

export function QyWalletEntryCard() {
  const { t } = useTranslation()
  const config = useQyConfig()

  // 扩展关闭 / 全部入口关闭时**整张卡不渲染**，钱包页零痕迹回到上游形态。
  // 注意这里不看 `available`：后端 503 时入口仍应可见，点进去由页面自己
  // 显示"暂不可用"，比入口凭空消失更好排查。
  const entries = config.enabled ? ENTRIES.filter((e) => e.visible(config)) : []
  if (entries.length === 0) return null

  return (
    <Card data-card-hover='false' className='bg-muted/20 py-0'>
      <CardContent className='p-3 sm:p-4'>
        <h3 className='mb-2.5 text-sm font-semibold'>
          {t('qy_common_wallet_entry_title')}
        </h3>
        <div
          className={cn(
            'grid gap-2',
            entries.length > 1 && 'sm:grid-cols-2',
            entries.length > 2 && 'lg:grid-cols-3'
          )}
        >
          {entries.map((entry) => (
            <Link
              key={entry.key}
              to={entry.to}
              hash={entry.hash}
              className='bg-background hover:border-primary/50 focus-visible:ring-ring/50 flex min-w-0 items-center gap-2.5 rounded-lg border p-2.5 transition-colors focus-visible:ring-[3px] focus-visible:outline-none'
            >
              <IconBadge tone={entry.tone}>
                <entry.icon />
              </IconBadge>
              <div className='min-w-0 flex-1'>
                <div className='truncate text-sm font-medium'>
                  {t(entry.titleKey)}
                </div>
                <div className='text-muted-foreground truncate text-xs'>
                  {t(entry.descKey)}
                </div>
              </div>
              <ArrowRight
                className='text-muted-foreground size-4 shrink-0'
                aria-hidden='true'
              />
            </Link>
          ))}
        </div>
      </CardContent>
    </Card>
  )
}

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

/**
 * 钱包页 Tabs **之外**那一块的挂载点。
 *
 * 上游 `features/wallet/index.tsx` 里是一行 `<QyWalletSections />`。
 * 需求 5 之后这里只剩入口卡：划转已经进了 Tabs（触发器 + 面板各一行 JSX）。
 * 名字保留复数，是因为它仍是"Tabs 之外的 qy 内容"的唯一落点 —— 下一块
 * 要加的东西照样挂这里，而不是再往上游钱包页加第四行。
 */
export function QyWalletSections() {
  return <QyWalletEntryCard />
}

// 只再导出组件。取值常量与 hook 留在 `./tab`，由钱包页直接从那里 import：
// 组件文件里混着导出非组件会让 Vite 的 fast refresh 整个文件失效
// （oxlint 的 react/only-export-components 就是在盯这个）。
export { QyWalletTransferPanel } from '../pages/wallet-transfer'
