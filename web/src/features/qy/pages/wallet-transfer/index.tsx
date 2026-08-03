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
import { useTranslation } from 'react-i18next'

import { useQyConfig } from '../../hooks/use-qy-config'
import { QyPageTabs } from '../components/qy-page-tabs'
import { QyPayPasswordBody } from '../pay-password'
import { QyTransferBody } from '../transfer'
import { QyTransferLogsBody } from '../transfer-logs'

/**
 * 钱包页的「余额划转」板块（需求 2）。
 *
 * 项目方原话：「余额划转移动到钱包页面，选择夹新增一个余额划转板块。
 * 这个选择夹下包含：发起划转、划转记录、支付密码。」
 *
 * ── 为什么是钱包页里的一个板块，而不是钱包页顶部 Tabs 的第三格 ──
 * 上游那组 Tabs（充值 / 订阅套餐）**整条 TabsList 挂在 `showSubscriptionPanel`
 * 之下**：站点没配套餐时它压根不渲染。把划转塞进去，就等于让"有没有配订阅
 * 套餐"决定"能不能看到划转入口"，那是两件无关的事。而且改动会落在上游
 * `constants.ts`（tab 取值表）、`index.tsx`（触发器 + 面板）与钱包路由的
 * `validateSearch` 三个上游文件上。所以这里另起一个自带标题的板块，挂在
 * qy 已有的那个唯一挂载点上，上游钱包页的改动仍然是 1 个 import + 1 行 JSX。
 *
 * 标签状态走 URL hash（`#qy-transfer-logs`），原因见 `pages/lib/tabs.ts`。
 */
export function QyWalletTransferSection() {
  const { t } = useTranslation()
  const config = useQyConfig()

  // 与入口卡同一套开关：功能关掉、或站点把钱包入口关掉时整块不渲染。
  // 不看 `available`：扩展库降级时板块仍然在，由各标签自己显示"暂不可用"，
  // 比整块凭空消失更好排查（热路径 fail-open）。
  if (
    !config.enabled ||
    !config.features.transfer ||
    !config.wallet.show_transfer_entry
  ) {
    return null
  }

  return (
    <section className='space-y-3' aria-labelledby='qy-wallet-transfer-title'>
      <div className='space-y-0.5'>
        <h3 id='qy-wallet-transfer-title' className='text-sm font-semibold'>
          {t('qy_nav_transfer')}
        </h3>
        <p className='text-muted-foreground text-xs'>
          {t('qy_common_wallet_entry_transfer_desc')}
        </p>
      </div>
      <QyPageTabs
        host='/wallet'
        bodies={{
          '/qy/transfer': <QyTransferBody />,
          '/qy/transfer-logs': <QyTransferLogsBody />,
          '/qy/pay-password': <QyPayPasswordBody />,
        }}
      />
    </section>
  )
}
