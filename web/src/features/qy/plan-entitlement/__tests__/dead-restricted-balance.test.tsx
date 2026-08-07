/*
 * 「仅限」+ 零有效绑定（死钱）这条判据：**拦得住，且走得出去**。
 *
 * # 守什么
 *
 * 这条判据本身是对的 —— 余额范围设成「仅限」却一个仍然存在的模型分组都没绑，
 * 那笔钱任何请求都用不上，用户看得见、花不掉、到期作废。要守的是它的**作用范围**：
 *
 *   1. 危险组合仍然被抓住。零有效绑定的两种长相都要为真：清单空了，
 *      以及清单里只剩已从倍率表消失的存量名字（绑着等于没绑）。
 *   2. 它在抽屉内部**走得出去**。曾经的写法只有勾选框一个杠杆，而勾选框
 *      同时缩短"已选"和"孤儿"两个数组 —— 判据在候选清单为空时恒真，
 *      取消勾选也解不开，套餐从此永久存不下去。下面第 2 组用例逐个杠杆走一遍，
 *      只要没有任何一个能把它变回 false，就是恒真复发。
 *   3. 它挡的是**解锁清单那一次写入**，不是整张套餐表单。标题、价格、上下架
 *      落主库 subscription_plans，与这份落扩展库的绑定是两次没有事务的写入；
 *      把提交按钮串上这条判据，运营在别处删掉一个被引用的模型分组之后，
 *      这个套餐连标题都改不了 —— 而那正是本轮要修的形状，所以第 3 组直接
 *      钉住提交按钮的 disabled 表达式。
 *   4. 打开时就已经是死钱的套餐（运营在别处删的分组），只改标题保存时不该
 *      被指责：那次保存根本不碰这份绑定，返回 unchanged、不发请求。
 *
 * 取数走真实的 axios 实例（换掉 adapter），不 mock 模块：解包信封、错误归类
 * 这些都是这条链路的一部分，绕过它们就只是在测一份自己写的假数据。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'
import { parseSync } from 'oxc-parser'

import type { QyPlanEntitlement } from '../types'

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

const { act, useEffect } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { api } = await import('@/lib/api')
const { useQyPlanUnlockGroups } = await import('../use-plan-unlock-groups')
type HookResult = ReturnType<typeof useQyPlanUnlockGroups>

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/* ── 取数替身：真 axios 实例 + 假 adapter ───────────────────────────── */

type PutBody = { unlock_groups: string[]; balance_scope: string; note: string }

const entitlements = new Map<number, QyPlanEntitlement>()
const puts: { planId: number; body: PutBody }[] = []

function seed(planId: number, patch: Partial<QyPlanEntitlement>) {
  entitlements.set(planId, {
    plan_id: planId,
    plan_title: `plan-${planId}`,
    unlock_groups: [],
    balance_scope: 'universal',
    note: '',
    missing_groups: [],
    active_subscriptions: 0,
    ratio_table: [],
    balance_scope_enforced: true,
    enabled: true,
    model_group_candidates: [],
    ...patch,
  })
}

api.defaults.adapter = async (config) => {
  const url = String(config.url ?? '')
  const planId = Number(/plans\/(\d+)\/entitlement/.exec(url)?.[1] ?? 0)
  const current = entitlements.get(planId)
  assert.ok(current != null, `测试没有为套餐 ${planId} 准备数据：${url}`)
  if (String(config.method).toLowerCase() === 'put') {
    const body = JSON.parse(String(config.data)) as PutBody
    puts.push({ planId, body })
    seed(planId, {
      unlock_groups: body.unlock_groups,
      balance_scope: body.balance_scope as QyPlanEntitlement['balance_scope'],
      note: body.note,
    })
  }
  return {
    data: { success: true, message: '', data: entitlements.get(planId) },
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  }
}

/* ── 把 hook 挂起来，并把最新一次的返回值抓出来 ─────────────────────── */

const mounted: {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}[] = []

async function mountHook(planId: number) {
  const box = { current: null as HookResult | null }

  function Harness() {
    const result = useQyPlanUnlockGroups({
      open: true,
      planId,
      supported: true,
    })
    useEffect(() => {
      box.current = result
    })
    return null
  }

  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <Harness />
      </QueryClientProvider>
    )
  })
  // 取数是 effect 里的一个 promise，上面那一次 act 只跑到发出请求为止。
  await act(async () => {})
  mounted.push({ container, root })

  const read = () => {
    assert.ok(box.current != null, 'hook 还没有返回过任何值')
    return box.current
  }
  return {
    read,
    async run(action: (hook: HookResult) => void) {
      await act(async () => action(read()))
    },
    async save() {
      let outcome: Awaited<ReturnType<HookResult['saveIfChanged']>> | null =
        null
      await act(async () => {
        outcome = await read().saveIfChanged(planId)
      })
      return outcome
    },
  }
}

after(async () => {
  for (const { container, root } of mounted) {
    await act(async () => root.unmount())
    container.remove()
  }
})

/* ── 1. 危险组合仍然被抓住 ──────────────────────────────────────────── */

describe('死钱判据仍然拦得住', () => {
  test('「仅限」+ 清单为空', async () => {
    seed(101, { unlock_groups: [], balance_scope: 'restricted' })
    const hook = await mountHook(101)
    assert.equal(hook.read().restrictedWithoutBinding, true)
  })

  test('「仅限」+ 清单里只剩已从倍率表消失的存量名字', async () => {
    // 复核给出的失败场景：绑着 G，运营之后在「模型分组定价」里把 G 删了。
    seed(102, {
      unlock_groups: ['G'],
      model_group_candidates: [],
      balance_scope: 'restricted',
    })
    const hook = await mountHook(102)
    assert.deepEqual(hook.read().orphans, ['G'], 'G 应当被认成失效的存量绑定')
    assert.equal(
      hook.read().restrictedWithoutBinding,
      true,
      '绑着一个已经不存在的分组，等于没绑 —— 那笔余额照样花不掉'
    )
  })

  test('「通用」的套餐没绑任何分组也不是死钱', async () => {
    seed(103, { unlock_groups: [], balance_scope: 'universal' })
    const hook = await mountHook(103)
    assert.equal(hook.read().restrictedWithoutBinding, false)
  })
})

/* ── 2. 判据不是恒真：抽屉内部走得出去 ──────────────────────────────── */

describe('死钱状态在抽屉内部走得出去', () => {
  test('候选清单为空时，「改回通用」是唯一也是有效的出口', async () => {
    seed(201, {
      unlock_groups: ['G'],
      model_group_candidates: [],
      balance_scope: 'restricted',
    })
    const hook = await mountHook(201)
    assert.equal(hook.read().restrictedWithoutBinding, true)

    // 勾选框这个杠杆解不开：取消勾选让"已选"和"孤儿"同步缩短。
    // 这一条不是要求它解开，而是钉住"光靠勾选走不出去"这个事实 ——
    // 正因如此，下面那个出口必须存在。
    await hook.run((h) => h.toggle('G'))
    assert.equal(
      hook.read().restrictedWithoutBinding,
      true,
      '候选为空时取消勾选并不会产生有效绑定'
    )

    await hook.run((h) => h.resetScopeToUniversal())
    assert.equal(
      hook.read().restrictedWithoutBinding,
      false,
      '判据恒真了：候选清单是空的，勾选框里没有任何可勾的东西，' +
        '运营在这个抽屉内部再也无法让它变回 false'
    )
  })

  test('还有候选时，勾一个仍然存在的分组同样是出口', async () => {
    seed(202, {
      unlock_groups: ['A'],
      model_group_candidates: ['A', 'B'],
      balance_scope: 'restricted',
    })
    const hook = await mountHook(202)
    assert.equal(hook.read().restrictedWithoutBinding, false)

    await hook.run((h) => h.toggle('A'))
    assert.equal(
      hook.read().restrictedWithoutBinding,
      true,
      '摘掉最后一个有效绑定就是死钱，这一步必须被抓住'
    )

    await hook.run((h) => h.toggle('B'))
    assert.equal(
      hook.read().restrictedWithoutBinding,
      false,
      '勾上一个仍然存在的模型分组之后，这笔余额已经花得掉了'
    )
  })
})

/* ── 2b. 余额范围现在是抽屉里的一个真杠杆 ───────────────────────────── */

describe('余额使用范围在套餐表单里可选', () => {
  test('选「仅限」而没有任何有效绑定：当场变成死钱，且拒写', async () => {
    // 这条判据此前只可能被行操作那个弹窗触发（抽屉里范围是只读的）。
    // 范围搬进抽屉之后它多了一个入口，而入口多一个、拦不住就多一份死钱。
    seed(211, {
      unlock_groups: [],
      model_group_candidates: ['A'],
      balance_scope: 'universal',
    })
    const hook = await mountHook(211)
    const before = puts.length

    await hook.run((h) => h.setBalanceScope('restricted'))
    assert.equal(hook.read().restrictedWithoutBinding, true)
    assert.equal(await hook.save(), 'blocked')
    assert.equal(puts.length, before, '被挡住时不该有任何写入发出去')
  })

  test('选「仅限」并勾上一个仍然存在的分组：范围要真的落库', async () => {
    // 后端在缺 balance_scope 时按 universal 处理 —— 也就是静默把「仅限」改回
    // 「通用」。所以这次写入必须**带着新范围**，只发清单等于白改。
    seed(212, {
      unlock_groups: [],
      model_group_candidates: ['A'],
      balance_scope: 'universal',
    })
    const hook = await mountHook(212)

    await hook.run((h) => h.setBalanceScope('restricted'))
    await hook.run((h) => h.toggle('A'))
    assert.equal(hook.read().restrictedWithoutBinding, false)
    assert.equal(await hook.save(), 'saved')

    const last = puts.at(-1)
    assert.equal(last?.planId, 212)
    assert.equal(last?.body.balance_scope, 'restricted')
    assert.deepEqual(last?.body.unlock_groups, ['A'])
  })

  test('只改范围、不动清单：同样要落库', async () => {
    seed(213, {
      unlock_groups: ['A'],
      model_group_candidates: ['A'],
      balance_scope: 'universal',
    })
    const hook = await mountHook(213)

    await hook.run((h) => h.setBalanceScope('restricted'))
    assert.equal(await hook.save(), 'saved')
    assert.equal(puts.at(-1)?.body.balance_scope, 'restricted')
  })
})

/* ── 3. 挡的是那一次写入，不是整张表单 ──────────────────────────────── */

describe('死钱只挡解锁清单那一次写入', () => {
  test('亲手把状态改成死钱：拒写，且不发请求', async () => {
    seed(301, {
      unlock_groups: ['A'],
      model_group_candidates: ['A'],
      balance_scope: 'restricted',
    })
    const hook = await mountHook(301)
    const before = puts.length

    await hook.run((h) => h.toggle('A'))
    assert.equal(await hook.save(), 'blocked')
    assert.equal(puts.length, before, '被挡住时不该有任何写入发出去')
  })

  test('打开时就已经是死钱、这次没动它：静默放过，不发请求', async () => {
    // 运营只是来改标题的。指责他一次他并没有做过的改动，等于教他忽略这条提示。
    seed(302, {
      unlock_groups: ['G'],
      model_group_candidates: [],
      balance_scope: 'restricted',
    })
    const hook = await mountHook(302)
    const before = puts.length

    assert.equal(hook.read().restrictedWithoutBinding, true)
    assert.equal(await hook.save(), 'unchanged')
    assert.equal(puts.length, before)
  })

  test('只点了「改回通用」也要落库', async () => {
    // 只比对解锁清单的话，这一步会被判成"没变化"而短路：界面显示已改回通用，
    // 库里还是「仅限」—— 而这正是运营为了救那笔死钱做的唯一一件事。
    seed(303, {
      unlock_groups: ['G'],
      model_group_candidates: [],
      balance_scope: 'restricted',
    })
    const hook = await mountHook(303)

    await hook.run((h) => h.resetScopeToUniversal())
    assert.equal(await hook.save(), 'saved')

    const last = puts.at(-1)
    assert.equal(last?.planId, 303)
    assert.equal(last?.body.balance_scope, 'universal')
    assert.deepEqual(
      last?.body.unlock_groups,
      ['G'],
      '范围改回通用不该顺手把存量绑定也抹掉'
    )
  })
})

/* ── 4. 提交按钮没有被这条判据串上 ──────────────────────────────────── */

const drawerPath = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  'subscriptions',
  'components',
  'subscriptions-mutate-drawer.tsx'
)

/** 套餐编辑抽屉里那个提交按钮的 `disabled=` 表达式原文。 */
function submitButtonDisabledSource(): string {
  const source = readFileSync(drawerPath, 'utf8')
  const parsed = parseSync(drawerPath, source)
  assert.deepEqual(parsed.errors, [], `解析失败：${drawerPath}`)

  const found: string[] = []
  const visit = (node: unknown) => {
    if (node == null || typeof node !== 'object') return
    if (Array.isArray(node)) {
      for (const child of node) visit(child)
      return
    }
    const current = node as Record<string, unknown>
    if (current.type === 'JSXOpeningElement') {
      const attrs = (current.attributes ?? []) as Record<string, unknown>[]
      const named = (want: string) =>
        attrs.find(
          (a) => (a.name as { name?: string } | undefined)?.name === want
        )
      const type = named('type')?.value as { value?: string } | undefined
      const disabled = named('disabled')
      if (type?.value === 'submit' && disabled != null) {
        const span = disabled.value as { start: number; end: number }
        found.push(source.slice(span.start, span.end))
      }
    }
    for (const value of Object.values(current)) visit(value)
  }
  visit(parsed.program)

  assert.equal(
    found.length,
    1,
    `抽屉里应当只有一个带 disabled 的提交按钮，找到 ${found.length} 个`
  )
  return found[0]
}

describe('套餐表单的提交按钮', () => {
  test('不被解锁分组那一格的死钱判据禁用', () => {
    assert.ok(
      !submitButtonDisabledSource().includes('restrictedWithoutBinding'),
      '标题、价格、上下架落主库，与落扩展库的解锁绑定是两次没有事务的写入；' +
        '让整张表单为解锁那一格陪葬，运营在别处删掉一个被引用的模型分组之后，' +
        '这个套餐连标题都改不了'
    )
  })
})

/* ── 5. 抽屉确实把这两件事摆出来了 ──────────────────────────────────── */

describe('套餐表单说清了这笔额度怎么用', () => {
  const drawerSource = () => readFileSync(drawerPath, 'utf8')

  test('余额使用范围在抽屉里可选，不是只读', () => {
    const source = drawerSource()
    assert.ok(
      source.includes('QyPlanBalanceScopeField'),
      '「这笔余额能花在哪」与「解锁哪些分组」是同一个决定的两半：' +
        '范围退回另一个弹窗之后，运营会在零绑定的套餐上把它设成「仅限」，' +
        '而那是一份用户看得见、花不掉、到期作废的死钱'
    )
    assert.ok(
      source.includes('unlockGroups.setBalanceScope'),
      '选择器必须接在 hook 的 setBalanceScope 上 —— 接在别处的本地 state 上，' +
        '选出来的值不会进入 saveIfChanged 的比对，界面改了而库里没改'
    )
  })

  test('额度用尽后的两种后果都写出来了', () => {
    const source = drawerSource()
    for (const key of [
      'qy_plan_wallet_overflow_on_hint',
      'qy_plan_wallet_overflow_off_hint',
    ]) {
      assert.ok(
        source.includes(key),
        `缺 ${key}：这个开关此前只有一个标签、不勾的后果一个字都没写，` +
          '而不勾的后果是持有该套餐的用户被「订阅额度不足」挡下、' +
          '连自己的钱包余额也不许用'
      )
    }
  })
})
