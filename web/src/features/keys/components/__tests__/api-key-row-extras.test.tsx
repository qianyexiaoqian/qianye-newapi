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
 * 密钥列表新增的两格：「今日消耗」与「分组行内快切」。
 *
 * 这里只验会**静默出错**的那几条，每一条都是钱或数据：
 *
 *   ① 行内快切发出去的请求必须是 `?group_only=true` + 只有 {id, group}。
 *      走默认的整表替换会把令牌名清空、有效期写成 0（= 立刻过期）、额度清零，
 *      而接口返回 200 —— 用户看到"切换成功"，第二天发现密钥全废了。
 *   ② 后端拒绝时，下拉必须停在原分组。显示成新分组而库里没改，
 *      是这条链路上唯一会让用户按错误倍率估算成本的形状。
 *   ③ 候选清单必须与编辑抽屉同源（同一把 react-query key、同一个 builder）。
 *   ④ 「今天没花钱」要显示 0，「查不到」要显示 —。把查不到画成 0 是编金额。
 *   ⑤ 两格的 cell 都必须是稳定的组件引用，否则 flexRender 每次重挂，
 *      正在飞的那次切换会把结果写进已卸载的实例（见 api-keys-columns.tsx）。
 */
import assert from 'node:assert/strict'

import { Window } from 'happy-dom'

import upstreamEn from '@/i18n/locales/en.json'

const domWindow = new Window({ width: 1440, height: 900 })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'HTMLButtonElement',
  'HTMLInputElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'KeyboardEvent',
  'MouseEvent',
  'PointerEvent',
  'MutationObserver',
  'ResizeObserver',
  'IntersectionObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
  'matchMedia',
  'DOMRect',
] as const

for (const key of domGlobals) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}

/*
 * ── 为什么这一个文件的 describe/test 来自 `bun:test` 而不是本仓通用的 `node:test` ──
 *
 * `src/features/keys` 这个跑测单元是**一个** bun 进程跑 7 个文件。第一个文件
 * (api-key-group-cell.test.tsx) 的用例是异步的，等它跑完之前，后面每个文件的
 * 顶层 `describe` 都会撞上 bun 那条 node:test 垫片的未实现分支
 * (`describe() inside another test()`，oven-sh/bun#5090)，整份文件被静默吞掉 ——
 * 实测本目录原有 6 个文件里有 5 个从来没跑过，而它们全被记在
 * `scripts/run-tests.mjs` 的 KNOWN_FAILURES 里当成"上游自带的失败"。
 *
 * `bun:test` 走 bun 自己的注册表，不经过那条垫片。这不是风格偏好：用 node:test
 * 写的话，下面九条用例**一条都不会跑**，而闸门仍然是绿的（失败数照样对得上
 * KNOWN_FAILURES）—— 正是本仓反复出现的"写了但没接上"。run-tests.mjs 的文件头
 * 也把它列为当前唯一可行的规避方式。
 *
 * 用 `await import` 而不是静态 import 是被排序器逼的：`bun:` 排在所有前缀之前，
 * 静态写法会被提到**版权头之上**，copyright:check 立刻变红。
 */
const { afterAll, describe, test } = await import('bun:test')
const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: upstreamEn } } })

const { api } = await import('@/lib/api')
const { TooltipProvider } = await import('@/components/ui/tooltip')
const { ApiKeysProvider } = await import('../api-keys-provider')
const { ApiKeyGroupSwitchCell } = await import('../api-key-group-switch-cell')
const { ApiKeyTodayUsageCell } = await import('../api-key-today-usage-cell')
const { useApiKeysColumns } = await import('../api-keys-columns')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

type Recorded = { url: string; method: string; body: unknown }

/** 本轮记录下来的请求。每次 mount 前清空。 */
let recorded: Recorded[] = []
/** `PUT /api/token/` 的下一个应答。默认成功并回显请求里的分组。 */
let nextPutResponse: { success: boolean; message?: string; group?: string } = {
  success: true,
}
/** 「今日消耗」端点这一轮是正常返回还是 503。 */
let todayUsageMode: 'fail' | 'ok' = 'ok'

/*
 * 用 axios adapter 打桩而不是 mock 掉 `../api` 模块：这条用例要证明的第一件事
 * 就是**发出去的 URL 与请求体**长什么样，把请求层 mock 掉等于把被验的东西删了。
 */
api.defaults.adapter = async (config) => {
  const url = config.url ?? ''
  const body =
    typeof config.data === 'string' ? JSON.parse(config.data) : config.data
  recorded.push({ url, method: (config.method ?? 'get').toLowerCase(), body })

  const reply = (data: unknown) => ({
    data,
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  })

  if (url.startsWith('/api/qy/token-usage/today')) {
    if (todayUsageMode === 'fail') {
      // 503：扩展库不可用。qyGet 会把它翻成 QyError('unavailable') 并抛出，
      // 于是这一列进错误态 —— 那正是"金额未知"必须显示成未知的那一刻。
      throw {
        response: {
          status: 503,
          data: { success: false, code: 'qy_unavailable' },
        },
      }
    }
    return reply({
      success: true,
      data: {
        // 服务器本地自然日的一天(这里取 UTC-07:00 的 2026-08-22),
        // 与真实端点的字段逐字一致 —— 换字段名时这一格必须一起改,
        // 否则组件读到 undefined 只会静默显示成 UTC。
        day_start: 1787382000,
        day_end: 1787468400,
        timezone: 'PDT',
        utc_offset_minutes: -420,
        index_ready: true,
        usage: { '42': 250000 },
      },
    })
  }
  if (url.startsWith('/api/user/self/groups')) {
    return reply({
      success: true,
      data: {
        vip: { desc: 'Priority group', ratio: 3 },
        default: { desc: 'Standard group', ratio: 1 },
        free: { desc: 'Free group', ratio: 0 },
      },
    })
  }
  if (url.startsWith('/api/token/') && config.method?.toLowerCase() === 'put') {
    if (!nextPutResponse.success) {
      return reply({ success: false, message: nextPutResponse.message })
    }
    const requested = (body as { group?: string } | undefined)?.group ?? ''
    return reply({
      success: true,
      data: { id: 42, group: nextPutResponse.group ?? requested },
    })
  }
  return reply({ success: true, data: {} })
}

const API_KEY = {
  id: 42,
  name: 'my key',
  key: 'abcd',
  status: 1,
  remain_quota: 500000,
  used_quota: 0,
  unlimited_quota: false,
  expired_time: -1,
  created_time: 1700000000,
  accessed_time: 1700000000,
  group: 'default',
  auto_groups: null,
  cross_group_retry: false,
  model_limits_enabled: false,
  model_limits: '',
  allow_ips: '',
}

let mounted: {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
} | null = null

async function unmount() {
  if (mounted == null) return
  const current = mounted
  mounted = null
  await act(async () => current.root.unmount())
  current.container.remove()
}

afterAll(async () => {
  await unmount()
  domWindow.close()
})

async function mount(node: React.ReactNode) {
  await unmount()
  recorded = []
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <TooltipProvider>
          <ApiKeysProvider>{node}</ApiKeysProvider>
        </TooltipProvider>
      </QueryClientProvider>
    )
  })
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 30))
  })
  mounted = { container, root }
  return container
}

const row = { original: API_KEY } as never

function groupTrigger(container: ParentNode): HTMLButtonElement {
  const trigger = container.querySelector<HTMLButtonElement>(
    'button[data-api-key-group-switch="trigger"]'
  )
  assert.ok(trigger, '分组格里没有可点开的触发器')
  return trigger
}

function commandItem(label: string): HTMLElement {
  const item = [
    ...document.querySelectorAll<HTMLElement>('[data-slot="command-item"]'),
  ].find((candidate) => candidate.textContent?.includes(label))
  assert.ok(item, `下拉里没有 ${label}`)
  return item
}

function putRequests(): Recorded[] {
  return recorded.filter(
    (r) => r.method === 'put' && r.url.startsWith('/api/token/')
  )
}

describe('分组行内快切', () => {
  test('只发 group_only，请求体里只有 id 与 group', async () => {
    nextPutResponse = { success: true }
    const container = await mount(<ApiKeyGroupSwitchCell row={row} />)

    await act(async () => groupTrigger(container).click())
    await act(async () => commandItem('Priority group').click())
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 30))
    })

    const puts = putRequests()
    assert.equal(puts.length, 1, '一次切换只能发一条写请求')
    assert.equal(
      puts[0].url,
      '/api/token/?group_only=true',
      '少了 group_only 就是整表替换：令牌名会被清空、有效期会被写成 0'
    )
    assert.deepEqual(
      puts[0].body,
      { id: 42, group: 'vip' },
      '请求体里多一个字段就意味着那个字段会被写回库里'
    )
    assert.equal(
      groupTrigger(container).textContent?.includes('vip'),
      true,
      '后端确认之后，这一格要立刻显示新分组，不能等列表刷回来'
    )
  })

  test('后端拒绝时停在原分组', async () => {
    nextPutResponse = { success: false, message: '无权访问 vip 分组' }
    const container = await mount(<ApiKeyGroupSwitchCell row={row} />)

    await act(async () => groupTrigger(container).click())
    await act(async () => commandItem('Priority group').click())
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 30))
    })

    assert.equal(putRequests().length, 1)
    const text = groupTrigger(container).textContent ?? ''
    assert.equal(
      text.includes('default'),
      true,
      '被拒之后必须回到原分组 —— 显示成功而库里没改会让用户按错误的倍率估成本'
    )
    assert.equal(text.includes('vip'), false)
  })

  test('候选清单与编辑抽屉同源，并按名字升序', async () => {
    nextPutResponse = { success: true }
    const container = await mount(<ApiKeyGroupSwitchCell row={row} />)

    await act(async () => groupTrigger(container).click())
    const labels = [
      ...document.querySelectorAll<HTMLElement>('[data-slot="command-item"]'),
    ].map((item) => item.getAttribute('data-value'))
    assert.deepEqual(
      labels,
      ['default', 'free', 'vip'],
      '清单必须是 buildApiKeyGroupOptions 作用在 /api/user/self/groups 上的结果，' +
        '与编辑抽屉逐位相同；顺序也必须稳定，否则刷新之后用户会点错'
    )

    const groupCalls = recorded.filter((r) =>
      r.url.startsWith('/api/user/self/groups')
    )
    assert.equal(groupCalls.length, 1, '整张表只能取一次可选清单，不是每行一次')
  })

  test('倍率 0 的分组显示 0x，而不是不显示', async () => {
    nextPutResponse = { success: true }
    const container = await mount(<ApiKeyGroupSwitchCell row={row} />)

    await act(async () => groupTrigger(container).click())
    await act(async () => commandItem('Free group').click())
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 30))
    })

    const text = groupTrigger(container).textContent ?? ''
    assert.equal(
      text.includes('0x'),
      true,
      '「显式配成 0」与「没配」是两个状态：0 倍率必须画出来'
    )
    assert.equal(
      container.querySelector('[data-api-key-group-ratio="unknown"]'),
      null,
      '有倍率的分组不该同时挂着「未知」标记'
    )
  })

  test('不在可选清单里的分组显示未知，而不是 1x', async () => {
    nextPutResponse = { success: true }
    const orphan = { original: { ...API_KEY, group: 'retired-pool' } } as never
    const container = await mount(<ApiKeyGroupSwitchCell row={orphan} />)

    assert.ok(
      container.querySelector('[data-api-key-group-ratio="unknown"]'),
      '后端在查不到分组倍率时是 fail-open 返回 1 的，界面上绝不能印那个 1'
    )
    assert.equal(
      container.querySelector('button[data-api-key-group-switch] div'),
      null,
      '触发器是一个 <button>，里面出现 <div> 就是不合法的嵌套 —— ' +
        'ApiKeyGroupCell 必须走 inline 那一档'
    )
    assert.equal(
      (groupTrigger(container).textContent ?? '').includes('1x'),
      false
    )
  })

  test('行实例被另一把密钥复用时，绝不显示上一把的分组与倍率', async () => {
    /*
      这张表**没有**传 `getRowId`，tanstack 于是回落到「row.id = 行序号」，
      桌面表格 (data-table-view.tsx) 与手机卡片 (api-keys-table.tsx) 两条渲染
      路径的 key 都是 `row.id`。列表查询又开着 `placeholderData: previousData`，
      翻页 / 搜索 / 删掉上面一行都不会让整行卸载重挂 —— 同一个组件实例连同它
      的本地 state 会原地换上另一把密钥。

      下面用「同一个 root 上换 row 属性再渲染」精确复刻这件事（React 认出同一
      位置的同一种组件，保留 state，与表格里发生的事逐位相同）。

      判据不能只有分组名：这一格连倍率一起显示，而这一列存在的唯一理由就是
      让用户知道自己按几倍在花钱。上一把切到 vip(3x) 之后，新上来这把库里是
      default(1x)，显示成 vip 3x 就是给用户报了一个错误的价。
    */
    nextPutResponse = { success: true }
    const first = {
      original: { ...API_KEY, id: 42, group: 'default' },
    } as never
    const container = await mount(<ApiKeyGroupSwitchCell row={first} />)

    await act(async () => groupTrigger(container).click())
    await act(async () => commandItem('Priority group').click())
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 30))
    })
    assert.equal(
      (groupTrigger(container).textContent ?? '').includes('vip'),
      true,
      '前置条件不成立：这一把根本没切成功，后面的复用判据就没有意义'
    )

    // 换上另一把密钥：它库里的分组恰好等于上一把切换前的 base（同一用户多把
    // 密钥同分组是常态，所以这是最常撞上的形状，不是刻意构造的巧合）。
    const second = {
      original: { ...API_KEY, id: 43, name: 'another key', group: 'default' },
    } as never
    await act(async () => {
      mounted?.root.render(
        <QueryClientProvider
          client={
            new QueryClient({ defaultOptions: { queries: { retry: false } } })
          }
        >
          <TooltipProvider>
            <ApiKeysProvider>
              <ApiKeyGroupSwitchCell row={second} />
            </ApiKeysProvider>
          </TooltipProvider>
        </QueryClientProvider>
      )
    })
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 30))
    })

    const text = groupTrigger(container).textContent ?? ''
    assert.equal(
      text.includes('vip'),
      false,
      `第 43 号密钥库里是 default，却显示成了上一把切换后的分组：${JSON.stringify(text)}`
    )
    assert.equal(
      text.includes('3x'),
      false,
      `倍率跟着串了行：用户会照这个数估成本。实际显示 ${JSON.stringify(text)}`
    )
    assert.equal(
      text.includes('default'),
      true,
      '换上来的这把密钥必须显示它自己库里的分组'
    )
  })
})

describe('今日消耗', () => {
  test('今天没花钱的密钥显示 0，不是 -', async () => {
    todayUsageMode = 'ok'
    const container = await mount(
      <ApiKeyTodayUsageCell
        row={{ original: { ...API_KEY, id: 99 } } as never}
      />
    )
    assert.equal(
      container.textContent?.trim(),
      '$0',
      '缺席 = 今天一次都没用过 = 0；画成 "-" 会让用户以为这一列没做完'
    )
  })

  test('有消费时按与额度列同一个格式化函数显示', async () => {
    todayUsageMode = 'ok'
    const container = await mount(<ApiKeyTodayUsageCell row={row} />)
    // 250000 额度单位 / 500000 每美元 = $0.5，与同表「额度」列同一个 formatQuota。
    assert.equal(container.textContent?.trim(), '$0.5')
  })

  test('取不到时显示 —，绝不显示 0', async () => {
    todayUsageMode = 'fail'
    const container = await mount(<ApiKeyTodayUsageCell row={row} />)
    const text = container.textContent?.trim() ?? ''
    assert.equal(
      text,
      '—',
      `金额未知时必须显示成未知，实际显示的是 ${JSON.stringify(text)}`
    )
  })
})

describe('两格的 cell 必须是稳定的组件引用', () => {
  test('同一个 now 下重复渲染，group 与 today_usage 的 cell 不变', async () => {
    const seen: { group: unknown; today: unknown }[] = []
    function Probe() {
      const columns = useApiKeysColumns(1_700_000_000_000)
      /*
        分组列声明的是 `accessorKey: 'group'`，**没有**显式 id —— id 是
        TanStack 在 useReactTable 里派生出来的，而这里拿到的是没被处理过的
        原始 ColumnDef 数组。按 `column.id === 'group'` 找会恒为 undefined，
        于是下面两边都是 undefined、断言恒真：这条守卫写错一次就是一条永绿的
        假测试（实测：把 cell 改回内联箭头它照样 9 pass）。
      */
      const group = columns.find(
        (column) => 'accessorKey' in column && column.accessorKey === 'group'
      )
      const today = columns.find((column) => column.id === 'today_usage')
      assert.ok(group?.cell, '分组列不见了，这条守卫没有被守的东西')
      assert.ok(today?.cell, '今日消耗列不见了，这条守卫没有被守的东西')
      seen.push({ group: group.cell, today: today.cell })
      return null
    }
    todayUsageMode = 'ok'
    await mount(<Probe />)
    await act(async () => {
      mounted?.root.render(
        <QueryClientProvider
          client={
            new QueryClient({ defaultOptions: { queries: { retry: false } } })
          }
        >
          <TooltipProvider>
            <ApiKeysProvider>
              <Probe />
            </ApiKeysProvider>
          </TooltipProvider>
        </QueryClientProvider>
      )
    })

    assert.ok(seen.length >= 2, '探针没渲染两次')
    assert.equal(
      seen[0]?.group,
      seen.at(-1)?.group,
      '分组格写成了内联箭头：flexRender 会把它当成新组件类型，' +
        '正在飞的那次切换会把结果写进一个已卸载的实例'
    )
    assert.equal(
      seen[0]?.today,
      seen.at(-1)?.today,
      '今日消耗格写成了内联箭头：每 30 秒会把每一行的查询订阅卸载重挂一次'
    )
  })
})
