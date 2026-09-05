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
 * D-16 之后只剩 `done`：入账是扩展库里的一个本地事务，佣金余额的搬运与星屑流水
 * 的写入在同一次提交里，不存在"这一半成了那一半没成"。D-15 的 pending / failed /
 * held 描述的全是跨库两阶段的中间与失败形状，那套机制已经退役。
 *
 * 表保留成一张查找表而不是塌成一个常量：后端将来加态时这里加一行即可，而
 * 下面的回落分支保证多出来的态只是"不好看"（中性徽章 + 原样字符串），绝不崩。
 */
const CREDIT_BADGE: Record<
  QyCommissionCreditStatus,
  {
    labelKey: string
    variant: 'destructive' | 'outline' | 'secondary' | 'warning'
  }
> = {
  done: { labelKey: 'qy_aff_credit_st_done', variant: 'secondary' },
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
