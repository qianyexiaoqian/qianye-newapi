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
 * 扣费顺序写死为「套餐优先」之后：入口没了，说明在，页面其余部分照旧。
 *
 * # 守什么
 *
 * 「计费偏好」曾经是钱包页上一个四选一的下拉（订阅优先 / 钱包优先 / 仅用订阅 /
 * 仅用钱包），用户自己就能改，改完直接决定这笔钱从套餐还是从钱包出。这一轮把
 * 顺序写死：先套餐、套餐出不了资才到钱包，用户不再有发言权。
 *
 *   1. **入口真的不存在**（源码级）。这一条只能在源码上断言，不能靠渲染：
 *      下拉是不是被某个条件分支藏起来了、`updateBillingPreference` 是不是还挂在
 *      别的按钮上，渲染测试都看不出来。而它被加回来的代价是真钱 —— 后端那条
 *      `PUT /api/subscription/self/preference` 同批撤掉了，重新接上去的下拉会
 *      静默失败：界面弹"保存成功"，扣费顺序一分没变。
 *   2. **说明文案真的渲染出来了**。撤掉一个用过的开关而不解释，用户看到的是
 *      "我设的仅用钱包不见了、现在钱从哪扣不知道"。所以断言那句静态说明确实在
 *      那一格里，而不只是躺在语言包里。
 *   3. **那一屏没被拆坏**。删下拉顺手删掉刷新按钮 / 订阅列表 / 套餐卡的形状，
 *      typecheck 一样是绿的，所以这三样在同一次渲染里逐个点名。
 *
 * i18n 用空 resources 初始化，`t('x')` 回落成键名本身，断言直接打在键名上：
 * 文案改写不会把测试改红，键被删掉会。
 */
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'
import { parseSync } from 'oxc-parser'

// __tests__ → wallet → features → src
const srcDir = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..')

const domWindow = new Window()
const domGlobals = [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: {} } } })

const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { api } = await import('@/lib/api')
const { SubscriptionPlansCard } =
  await import('../components/subscription-plans-card')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/* ── 1. 入口不存在（源码级） ─────────────────────────────────────────── */

function collectSources(dir: string): string[] {
  const files: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name !== 'node_modules') files.push(...collectSources(full))
      continue
    }
    if (/\.tsx?$/.test(entry.name)) files.push(full)
  }
  return files
}

/**
 * 一个文件里**代码**用到的名字与字符串字面量（注释不算）。
 *
 * 判据必须落在 AST 上而不是原文：撤掉一个东西之后，讲清楚"这里为什么不再有它"
 * 的墓碑注释本身就会提到那个名字（`api.ts` 与 `types.ts` 各留了一段），
 * 按原文匹配的话，越是把话说清楚的改动越会把守卫弄红，最后被删掉的是守卫。
 */
function codeTokens(file: string, source: string): Set<string> {
  const parsed = parseSync(file, source)
  assert.deepEqual(parsed.errors, [], `解析失败：${file}`)

  const tokens = new Set<string>()
  const visit = (node: unknown) => {
    if (node == null || typeof node !== 'object') return
    if (Array.isArray(node)) {
      for (const child of node) visit(child)
      return
    }
    const current = node as Record<string, unknown>
    // 标识符 / JSX 名字走 name，字符串字面量与模板片段走 value / raw。
    // 宁可多收（JSX 文本也会进来）也不少收：多收只会让守卫更严。
    if (typeof current.name === 'string') tokens.add(current.name)
    if (typeof current.value === 'string') tokens.add(current.value)
    if (typeof current.raw === 'string') tokens.add(current.raw)
    for (const value of Object.values(current)) {
      if (value != null && typeof value === 'object') visit(value)
    }
  }
  visit(parsed.program)
  return tokens
}

/** 整个 src/ 的 .ts/.tsx，除本文件自身（它必须提到那些名字才能守它们）。 */
const otherSources = collectSources(srcDir)
  .filter((f) => f !== fileURLToPath(import.meta.url))
  .map((f) => ({
    path: relative(srcDir, f),
    text: readFileSync(f, 'utf8'),
  }))
  .map((f) => ({ ...f, tokens: codeTokens(f.path, f.text) }))

function usedBy(...names: string[]): string[] {
  const hits: string[] = []
  for (const f of otherSources) {
    for (const name of names) {
      if (f.tokens.has(name)) hits.push(`${name} <- ${f.path}`)
    }
  }
  return hits
}

describe('计费偏好的用户入口已经撤干净', () => {
  test('没有任何代码再提 billing_preference', () => {
    assert.deepEqual(
      usedBy('billing_preference'),
      [],
      '扣费顺序写死为「套餐优先」之后，billing_preference 不再有任何读者：' +
        '后端不读、日志详情页不读、self 接口也不再下发。这里出现引用，' +
        '说明有人把一个写了不生效的设置接回了界面'
    )
  })

  test('没有任何代码再打 PUT /api/subscription/self/preference', () => {
    assert.deepEqual(
      usedBy('/api/subscription/self/preference', 'updateBillingPreference'),
      [],
      '这条路由已随后端一起撤掉。重新接上去的下拉会**静默**失败：' +
        '请求 404，界面照样弹"保存成功"，而扣费顺序一分没变'
    )
  })

  test('四个偏好取值的文案不再被任何组件引用', () => {
    assert.deepEqual(
      usedBy(
        'Subscription First',
        'Subscription Only',
        'Wallet First',
        'Wallet Only',
        'Preference saved as {{pref}}, but no active subscription. Wallet will be used automatically.'
      ),
      [],
      '这五个键已从 7 份语言包与 static-keys 里删掉；还有人 t() 它们的话，' +
        'i18next 会把键名原样渲染到界面上'
    )
  })

  test('钱包页的订阅卡里没有下拉框', () => {
    const card = otherSources.find(
      (f) =>
        f.path.replaceAll('\\', '/') ===
        'features/wallet/components/subscription-plans-card.tsx'
    )
    assert.ok(card != null, '找不到订阅卡源码，路径变了就把这条守卫改对')
    assert.ok(
      !card.tokens.has('Select') && !card.tokens.has('SelectTrigger'),
      '这张卡里唯一存在过的 Select 就是计费偏好下拉。' +
        '新加下拉请顺手改这条断言，别让它替一个新的用户可改开关背书'
    )
  })
})

/* ── 2 & 3. 渲染：说明在，页面其余部分照旧 ──────────────────────────── */

const PLAN = {
  plan: {
    id: 7,
    title: 'plan-seven',
    subtitle: '',
    price: 100,
    max_purchase_per_user: 0,
    allow_wallet_overflow: true,
  },
}

const SUBSCRIPTION = {
  subscription: {
    id: 42,
    plan_id: 7,
    status: 'active',
    end_time: Math.floor(Date.now() / 1000) + 86400,
    amount_total: 1000,
    amount_used: 250,
    next_reset_time: 0,
  },
}

api.defaults.adapter = async (config) => {
  const url = String(config.url ?? '')
  if (url.includes('/api/subscription/plans')) {
    return {
      data: { success: true, message: '', data: [PLAN] },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    }
  }
  if (url.includes('/api/subscription/self')) {
    return {
      data: {
        success: true,
        message: '',
        data: {
          subscriptions: [SUBSCRIPTION],
          all_subscriptions: [SUBSCRIPTION],
        },
      },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    }
  }
  // 权益接口未启用是它的正常返回（404 + qy_feature_off），摘要静默隐藏。
  // 这里刻意用这一档：本测试要看的是订阅卡本身，不是权益披露。
  return {
    data: {
      success: false,
      message: 'off',
      code: 'qy_feature_off',
      data: null,
    },
    status: 404,
    statusText: 'Not Found',
    headers: {},
    config,
  }
}

const mounted: {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}[] = []

async function renderCard() {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <SubscriptionPlansCard topupInfo={null} />
      </QueryClientProvider>
    )
  })
  // 取数是 effect 里的 promise，上面那一次 act 只跑到发出请求为止。三条请求
  // （套餐、我的订阅、权益）还要各自过一轮微任务才写回 state，权益那条走的是
  // 被拒绝的 promise，再晚一个宏任务，所以这里两级都让一次。
  await act(async () => {})
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  mounted.push({ container, root })
  return container
}

after(async () => {
  // 卸载本身也是一次 React 更新：不包在 act 里，收尾时会打印一条
  // "not wrapped in act" 警告，而那条警告长得跟真的接线问题一模一样。
  await act(async () => {
    for (const { root } of mounted) root.unmount()
  })
  for (const { container } of mounted) container.remove()
})

describe('订阅卡在下拉撤掉之后', () => {
  test('扣费顺序的说明就在原来那一格里，且页面其余部分照旧', async () => {
    const container = await renderCard()
    const text = container.textContent ?? ''

    assert.ok(
      text.includes('qy_billing_order_fixed'),
      '撤掉一个用过的开关而不留说明，用户看到的是"我设的偏好不见了、' +
        '现在钱从哪扣不知道"。这句静态说明必须渲染在下拉原来所在的那一格里'
    )

    // 那一屏没被拆坏：标题、计数、刷新按钮、已购订阅、可购套餐各点名一次。
    for (const expected of [
      'Subscription Plans',
      'My Subscriptions',
      'plan-seven',
      'Subscribe Now',
    ]) {
      assert.ok(
        text.includes(expected),
        `删下拉不该动到 ${expected}：typecheck 对这类误删没有任何信号`
      )
    }
    assert.ok(
      container.querySelectorAll('button').length >= 2,
      '刷新按钮与「立即订阅」都得还在'
    )

    // 下拉真的不在 DOM 里（源码断言之外的第二道：Select 换个写法照样是下拉）。
    assert.equal(
      container.querySelectorAll('[role="combobox"]').length,
      0,
      '订阅卡里不该再有任何下拉'
    )
  })
})
