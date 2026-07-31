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

import { getSelf } from '@/lib/api'
import { useAuthStore, type AuthUser } from '@/stores/auth-store'

import { qyKeys } from '../lib/query-keys'

/**
 * 资金类操作成功后的统一收尾。**划转 / 佣金兑现 / 提现到账后必须调用。**
 *
 * 为什么要全量失效 `['qy']` 而不是精确失效：这些操作走跨库两阶段，
 * 前端无法知道哪些视图受影响（余额、限额、流水、佣金账户可能同时变），
 * 全量失效是唯一安全的策略。
 *
 * 为什么还要 `getSelf()`：主库的 `users.quota` 不在 qy 的 query 缓存里，
 * 它在 Zustand 的 auth-store。上游没有任何"全局刷新用户余额"的机制，
 * 不补这一步的话，划转成功后顶栏余额会一直是旧值。
 */
export function useQyAfterMoneyChange() {
  const queryClient = useQueryClient()

  return useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    try {
      const res = await getSelf()
      if (res?.success && res.data) {
        useAuthStore.getState().auth.setUser(res.data as AuthUser)
      }
    } catch {
      // 余额刷新失败不能阻断主流程：钱已经动了，用户下次进页面自然会拿到新值。
    }
    await queryClient.invalidateQueries({ queryKey: ['status'] })
  }, [queryClient])
}
