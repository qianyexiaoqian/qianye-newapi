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
import { getSelf } from '@/lib/api'
import { useAuthStore, type AuthUser } from '@/stores/auth-store'

/**
 * 余额显示的**唯一**真相源同步点。
 *
 * # 为什么需要这个模块
 *
 * `users.quota` 在前端只有一个落点：Zustand 的 auth-store。概览页的余额卡
 * (`features/dashboard/components/overview/summary-cards.tsx`) 和顶栏读的都是它。
 * 上游没有任何"全局刷新用户余额"的机制，所以每一条动钱的路径都得自己写回去。
 *
 * 历史上这件事被写坏过三次，形状都一样 —— **拉了新数据却没写回 store**：
 *
 *   · 兑换码充值：`await getSelf()`，返回值直接丢掉。那一行长得就像在刷新，
 *     所以能一路骗过 code review。
 *   · 佣金划转：同款裸 `await getSelf()`。
 *   · 钱包页：用了返回值，但写进的是组件自己的 `useState`。于是钱包页显示新
 *     余额、概览页显示旧余额 —— 正是"充值没到账"这个投诉的由来。
 *
 * 所以 `getSelf()` 在整个钱包特性里只允许出现在这个文件，回归测试
 * (`../__tests__/balance-refresh.test.ts`) 用 AST 守着这一点。
 */

/** `getSelf()` 的返回形状；本模块只关心这两个字段。 */
export type SelfResponse = {
  success?: boolean
  data?: unknown
} | null

export type SelfFetcher = () => Promise<SelfResponse>

/**
 * 重新拉取当前用户并写回 auth-store，返回新的用户对象（失败返回 `null`）。
 *
 * 返回值是给"还需要一份本地副本"的调用方用的（比如钱包页的统计卡），这样一次
 * 请求同时喂饱 store 和局部 state，不必再打第二个 `getSelf`。
 *
 * `fetchSelf` 只为测试注入：它是本模块唯一的网络边界。
 */
export async function refreshCurrentUser(
  fetchSelf: SelfFetcher = getSelf
): Promise<AuthUser | null> {
  let response: SelfResponse
  try {
    response = await fetchSelf()
  } catch {
    // 刷新失败不能阻断主流程：钱已经动了，用户下次进页面自然会拿到新值。
    return null
  }

  if (!response?.success || !response.data) return null

  const user = response.data as AuthUser
  useAuthStore.getState().auth.setUser(user)
  return user
}

/**
 * 订阅"从站外支付页回到本页"这个时刻，返回退订函数。
 *
 * # 为什么在线支付这条路必须靠它
 *
 * creem / stripe / epay / waffo 全都是**离开本页**去付款的：stripe 与 creem 走
 * `window.open(pay_link, '_blank')`，epay/waffo 走 `submitPaymentForm()` 提交一个
 * `target='_blank'` 的表单。也就是说钱是在**另一个标签页**里付的，到账走的是
 * 服务端回调，本页从头到尾没有任何事件。
 *
 * 于是原来那句"支付成功后 `await fetchUser()`"其实是在**发起跳转的瞬间**执行的，
 * 那时用户连支付页都还没看到，拉回来的必然是旧余额。这才是项目方说的"充值钱包"
 * 主路径上真正缺的一环。
 *
 * 只监听 `visibilitychange` 而不额外监听 `window.focus`：切回标签页时两者都会
 * 触发，一起挂等于每次回来都白打两个 `getSelf`。
 */
export function subscribeToTopupReturn(onReturn: () => void): () => void {
  const handleVisibilityChange = () => {
    if (document.visibilityState !== 'visible') return
    onReturn()
  }

  document.addEventListener('visibilitychange', handleVisibilityChange)
  return () => {
    document.removeEventListener('visibilitychange', handleVisibilityChange)
  }
}
