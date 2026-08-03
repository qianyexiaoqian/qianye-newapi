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
import { useCallback, useEffect, useState } from 'react'

import { AffiliateRewardsCard } from '@/features/wallet/components/affiliate-rewards-card'
import { TransferDialog } from '@/features/wallet/components/dialogs/transfer-dialog'
import { useAffiliate, useTopupInfo } from '@/features/wallet/hooks'
import type { UserWalletData } from '@/features/wallet/types'
import { getSelf } from '@/lib/api'

import { useQyAfterMoneyChange } from '../../../hooks/use-qy-after-money-change'

/**
 * 上游「推荐计划」卡在推广佣金页上的宿主。
 *
 * ── 为什么是"宿主"而不是重画一张 ──
 * 项目方要求把钱包页的推荐计划移到推广佣金页下。推荐计划**整块都是上游的**
 * （`aff_quota` / `aff_history_quota` / `aff_count` 三个数字、邀请链接、
 * 「转入余额」按钮，以及 `/api/user/aff_transfer` 这条接口），qy 只是换了个
 * 地方挂它。所以这里渲染的是**同一个** `AffiliateRewardsCard`，上游文件一行
 * 没改：照着它重画一张 qy 版，就是本仓反复出现的"同一概念的第 N 份拷贝"，
 * 上游改了字段这边不会跟着走。
 *
 * 钱包页那边对应地不再渲染它（`features/wallet/index.tsx`），一进一出由
 * `__tests__/referral-program-card.test.ts` 双向钉住 —— 只断言"推广页有"会
 * 放过"两边都有"，只断言"钱包页没有"会放过"被删干净了"。
 *
 * ── 与本页 `InviteLinkCard` 的关系 ──
 * 两者都显示邀请链接，但账本不是同一个：推荐计划卡上的三个数字来自主库
 * `users.aff_*`（上游返佣），而本页统计网格来自 qy 自己的佣金账本。刻意不合并，
 * 也刻意不互相取数 —— 把两个账本的数字混在一张卡里，对账时谁也说不清。
 */
export function QyReferralProgramCard() {
  const [user, setUser] = useState<UserWalletData | null>(null)
  const [userLoading, setUserLoading] = useState(true)
  const [transferDialogOpen, setTransferDialogOpen] = useState(false)

  const {
    affiliateLink,
    loading: affiliateLoading,
    transferQuota,
    transferring,
  } = useAffiliate()
  // 只为了 `payment_compliance_confirmed` 这一个开关：管理员没确认合规条款时
  // 「转入余额」必须是禁用的，缺了它按钮会变成点下去才被后端拒绝。
  const { topupInfo } = useTopupInfo()
  const afterMoneyChange = useQyAfterMoneyChange()

  const fetchUser = useCallback(async () => {
    try {
      setUserLoading(true)
      const response = await getSelf()
      if (response?.success && response.data) {
        setUser(response.data as UserWalletData)
      }
    } finally {
      setUserLoading(false)
    }
  }, [])

  useEffect(() => {
    void fetchUser()
  }, [fetchUser])

  const handleTransfer = async (amount: number) => {
    const success = await transferQuota(amount)
    if (!success) return false
    // 这一笔同时动了 `users.aff_quota` 与 `users.quota`：前者是本卡自己的三个
    // 数字，后者是顶栏余额与 qy 各视图。两处都要刷，少一处就会出现"钱到账了但
    // 页面上还是旧值"。
    await fetchUser()
    await afterMoneyChange()
    return true
  }

  return (
    <>
      <AffiliateRewardsCard
        user={user}
        affiliateLink={affiliateLink}
        onTransfer={() => setTransferDialogOpen(true)}
        complianceConfirmed={topupInfo?.payment_compliance_confirmed !== false}
        loading={userLoading || affiliateLoading}
      />
      <TransferDialog
        open={transferDialogOpen}
        onOpenChange={setTransferDialogOpen}
        onConfirm={handleTransfer}
        availableQuota={user?.aff_quota ?? 0}
        transferring={transferring}
      />
    </>
  )
}
