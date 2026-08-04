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
/*
 * 充值后余额要立刻刷新（需求 3）。
 *
 * # 守什么
 *
 * 项目方原话：「用户充值钱包后，这里的余额不会立即刷新，导致很多用户认为自己
 * 充值没有到账。」根因是三条动钱的路径都**拉了新数据却没写回 auth-store**，
 * 而概览页的余额卡读的正是 auth-store。
 *
 * 所以断言分两层，缺一不可：
 *
 *   1. **行为**：`refreshCurrentUser()` 之后 auth-store 里的 `quota` 必须是新值。
 *      只断言"调用了 getSelf"是没有意义的 —— 出问题的那版代码也调用了 getSelf，
 *      它只是把返回值丢掉了。唯一能证伪的是**store 里的数**。
 *   2. **接线（AST）**：`getSelf` 在整个钱包特性里只允许出现在 balance-refresh.ts。
 *      第 1 层测不到调用点：有人把 use-redemption.ts 改回裸 `await getSelf()`，
 *      行为测试照样全绿。这一层就是专门为那次回归准备的。
 */
import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { afterEach, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'
import { parseSync } from 'oxc-parser'

import { useAuthStore, type AuthUser } from '@/stores/auth-store'

import {
  refreshCurrentUser,
  subscribeToTopupReturn,
  type SelfResponse,
} from '../lib/balance-refresh'

// __tests__ → wallet
const walletDir = join(dirname(fileURLToPath(import.meta.url)), '..')

// `subscribeToTopupReturn` 挂的是 document 上的事件，需要一个 DOM。
const domWindow = new Window()
for (const key of ['window', 'document', 'Event'] as const) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const staleUser: AuthUser = {
  id: 7,
  username: 'topup-user',
  role: 1,
  quota: 100,
  used_quota: 0,
  request_count: 0,
}

function currentUser(): AuthUser | null {
  return useAuthStore.getState().auth.user
}

afterEach(() => {
  useAuthStore.getState().auth.reset()
})

/* ── 一、行为：新余额必须落进 auth-store ─────────────────────────────── */

describe('refreshCurrentUser 把新余额写回 auth-store', () => {
  test('成功响应后 store 里的 quota 是新值，并原样返回该用户', async () => {
    useAuthStore.getState().auth.setUser(staleUser)

    const returned = await refreshCurrentUser(async () => ({
      success: true,
      data: { ...staleUser, quota: 5000 },
    }))

    assert.equal(
      currentUser()?.quota,
      5000,
      'auth-store 还是旧余额：新数据被拉回来了却没有写回去 —— 概览页会继续显示"没到账"'
    )
    assert.equal(returned?.quota, 5000, '返回值要能直接喂给钱包页的局部 state')
  })

  test('响应 success=false 时保留旧值并返回 null', async () => {
    useAuthStore.getState().auth.setUser(staleUser)

    const returned = await refreshCurrentUser(async () => ({
      success: false,
      data: { ...staleUser, quota: 5000 },
    }))

    assert.equal(returned, null)
    assert.equal(
      currentUser()?.quota,
      100,
      '失败响应里的 data 不可信，不能覆盖掉 store'
    )
  })

  test('请求抛错时不冒泡，保留旧值并返回 null', async () => {
    useAuthStore.getState().auth.setUser(staleUser)

    const returned = await refreshCurrentUser(async () => {
      throw new Error('network down')
    })

    assert.equal(returned, null, '刷新失败不能阻断主流程：钱已经动了')
    assert.equal(currentUser()?.quota, 100)
  })

  test('没有 data 的响应不会把 store 清成空用户', async () => {
    useAuthStore.getState().auth.setUser(staleUser)

    const returned = await refreshCurrentUser(
      async () => ({ success: true }) as SelfResponse
    )

    assert.equal(returned, null)
    assert.equal(currentUser()?.username, 'topup-user')
  })
})

/* ── 二、在线支付：从站外付完款切回本页时要补刷 ───────────────────────── */

describe('subscribeToTopupReturn 覆盖"在另一个标签页付完款"', () => {
  function withVisibility(state: 'visible' | 'hidden') {
    Object.defineProperty(globalThis.document, 'visibilityState', {
      configurable: true,
      value: state,
    })
  }

  test('切回本页（visible）时触发回调', () => {
    let calls = 0
    const unsubscribe = subscribeToTopupReturn(() => {
      calls += 1
    })

    withVisibility('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    unsubscribe()

    assert.equal(
      calls,
      1,
      '这是在线支付路径上唯一的"付完款回来了"信号，丢了余额就永远停在旧值'
    )
  })

  test('切走本页（hidden）时不触发回调', () => {
    let calls = 0
    const unsubscribe = subscribeToTopupReturn(() => {
      calls += 1
    })

    withVisibility('hidden')
    document.dispatchEvent(new Event('visibilitychange'))
    unsubscribe()

    assert.equal(calls, 0, '离开页面时刷新既没意义又多打一个请求')
  })

  test('退订后不再触发（组件卸载不能留下野监听）', () => {
    let calls = 0
    const unsubscribe = subscribeToTopupReturn(() => {
      calls += 1
    })
    unsubscribe()

    withVisibility('visible')
    document.dispatchEvent(new Event('visibilitychange'))

    assert.equal(calls, 0)
  })
})

/* ── 三、接线：getSelf 只允许出现在 balance-refresh.ts ───────────────── */

function sourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      out.push(...sourceFiles(full))
      continue
    }
    if (/\.tsx?$/.test(entry.name)) out.push(full)
  }
  return out
}

type Symbols = {
  /** 从别处 import 进来的名字。 */
  imported: Set<string>
  /** 作为裸函数被调用过的名字。 */
  called: Set<string>
}

/** 走 AST 而不是正则：注释、字符串、类型标注里都会出现这些名字，正则版全是假绿。 */
function readSymbols(path: string): Symbols {
  const source = readFileSync(path, 'utf8')
  const parsed = parseSync(path, source)
  assert.deepEqual(parsed.errors, [], `解析失败：${path}`)

  const imported = new Set<string>()
  const called = new Set<string>()

  const visit = (node: unknown) => {
    if (node == null || typeof node !== 'object') return
    if (Array.isArray(node)) {
      for (const child of node) visit(child)
      return
    }
    const current = node as Record<string, unknown>

    if (current.type === 'ImportDeclaration') {
      for (const raw of (current.specifiers ?? []) as Record<
        string,
        unknown
      >[]) {
        const local = raw.local as Record<string, unknown> | undefined
        if (local?.name != null) imported.add(local.name as string)
      }
    }
    if (current.type === 'CallExpression') {
      const callee = current.callee as Record<string, unknown> | undefined
      if (callee?.type === 'Identifier') called.add(callee.name as string)
    }

    for (const value of Object.values(current)) visit(value)
  }
  visit(parsed.program)

  return { imported, called }
}

describe('钱包特性里 getSelf 的落点（AST）', () => {
  test('只有 lib/balance-refresh.ts 允许拿到 getSelf', () => {
    // 判据放在 import 而不是"调用"上，因为 balance-refresh 自己也不直接调它 ——
    // getSelf 只是那个可注入参数的默认值，真正的调用写作 `fetchSelf()`。
    // 而回归的形状恰恰是 `import { getSelf } from '@/lib/api'` 重新出现在某个
    // 调用点文件里，import 这一层刚好卡住它。
    const holders = sourceFiles(walletDir)
      .filter((path) => readSymbols(path).imported.has('getSelf'))
      .map((path) => relative(walletDir, path).replaceAll('\\', '/'))

    assert.deepEqual(
      holders,
      ['lib/balance-refresh.ts'],
      '又有文件直接抓 getSelf 了。这个形状历史上出现过三次，每次都是"拉了新数据' +
        '却没写回 auth-store"，界面上表现为充值后概览页余额不变。改用 refreshCurrentUser。'
    )
  })

  test('三条动钱的路径都经过 refreshCurrentUser', () => {
    for (const path of [
      'hooks/use-redemption.ts',
      'hooks/use-affiliate.ts',
      'index.tsx',
    ]) {
      assert.ok(
        readSymbols(join(walletDir, path)).called.has('refreshCurrentUser'),
        `${path} 没有调用 refreshCurrentUser：这条路径动完钱不会刷新 auth-store`
      )
    }
  })

  test('钱包页接上了"从站外支付回来"的订阅', () => {
    // 在线支付是离开本页去付的，本页收不到任何事件；少了这一句，主充值路径
    // （creem / stripe / epay / waffo）回来之后余额永远是发起支付前的旧值。
    assert.ok(
      readSymbols(join(walletDir, 'index.tsx')).called.has(
        'subscribeToTopupReturn'
      ),
      'wallet/index.tsx 没有调用 subscribeToTopupReturn'
    )
  })
})
