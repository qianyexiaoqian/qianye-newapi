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
import type { QyCommissionCreditStatus } from '../../affiliate/types'

/**
 * 自动入账单的状态 → 徽章。用户端「入账记录」与管理端「入账记录」共用一份，
 * 两处各画一遍就会出现"用户看到告警色、运营看到失败色"这种同一笔两种说法。
 *
 * `held` 用告警色而不是失败色：资金单结局不明、等人工裁决，钱可能已经进了
 * 星辉也可能没有；`failed` 是已确认没动、余额已退回可用。两者对看的人的下一步
 * 完全不同（等 / 什么都不用做），颜色不能一样。
 */
const CREDIT_BADGE: Record<
  QyCommissionCreditStatus,
  {
    labelKey: string
    variant: 'destructive' | 'outline' | 'secondary' | 'warning'
  }
> = {
  pending: { labelKey: 'qy_aff_credit_st_pending', variant: 'outline' },
  done: { labelKey: 'qy_aff_credit_st_done', variant: 'secondary' },
  failed: { labelKey: 'qy_aff_credit_st_failed', variant: 'destructive' },
  held: { labelKey: 'qy_aff_credit_st_held', variant: 'warning' },
}

export function qyCommissionCreditBadge(status: string): {
  labelKey: string
  variant: 'destructive' | 'outline' | 'secondary' | 'warning'
} {
  // 后端将来多一种状态时渲染成原样字符串（labelKey 落不到语言包，调用方给
  // defaultValue），而不是把整列打成裸键。
  return (
    CREDIT_BADGE[status as QyCommissionCreditStatus] ?? {
      labelKey: `qy_aff_credit_st_${status}`,
      variant: 'outline',
    }
  )
}
