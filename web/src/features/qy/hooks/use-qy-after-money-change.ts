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
import { useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'

// 深路径而不是 `@/features/wallet/lib` 桶：桶里带 `ui.tsx`，qy 不必把上游那堆
// JSX 一起拖进来。方向是 qy（扩展）→ wallet（上游核心），符合铁律。
import { refreshCurrentUser } from '@/features/wallet/lib/balance-refresh'

import { qyKeys } from '../lib/query-keys'

/**
 * 资金类操作成功后的统一收尾。**划转 / 佣金兑现 / 提现到账后必须调用。**
 *
 * 为什么要全量失效 `['qy']` 而不是精确失效：这些操作走跨库两阶段，
 * 前端无法知道哪些视图受影响（余额、限额、流水、佣金账户可能同时变），
 * 全量失效是唯一安全的策略。
 *
 * 为什么还要刷 auth-store：主库的 `users.quota` 不在 qy 的 query 缓存里，
 * 它在 Zustand 的 auth-store。上游没有任何"全局刷新用户余额"的机制，
 * 不补这一步的话，划转成功后顶栏余额会一直是旧值。
 *
 * 这一步**不在这里实现**，而是委托给 `refreshCurrentUser`：上游的充值路径
 * （兑换码 / 在线支付 / 钱包页）也要做同一件事，全仓只能有一份。反过来让上游
 * 去 import 这个 qy 钩子是不行的 —— qy 是可摘除的扩展，不能变成上游的支柱。
 */
export function useQyAfterMoneyChange() {
  const queryClient = useQueryClient()

  return useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    await refreshCurrentUser()
    await queryClient.invalidateQueries({ queryKey: ['status'] })
  }, [queryClient])
}
