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
 * 作用域卡在**页面上**的两件事。
 *
 * `ai-scope.test.ts` 覆盖的是纯逻辑(草稿往返、状态定性、名单切分)。逻辑对
 * 但卡片没有接进页面、或者接进去了却被一份缺字段的响应打成白屏 —— 这两种
 * 失效纯逻辑用例一条都看不见,而它们正是项目方打开这一页时会遇到的形态。
 *
 * 所以这里挂真组件、真 i18n:
 *
 *  1. 汇总表真的渲染出来了,而且表里**只有真实存在的策略行** —— 兜底档连同
 *     全局抽样率一起下线了,现在"没有任何策略命中"的答案恒为不审核,
 *     画一行恒为 0% 的假行只会让人以为那里还有个可调的旋钮。
 *     另外「送到哪个渠道」这一列必须在:指定的渠道被停用之后这一档不再审核
 *     任何内容,而它在列表上与一条正常策略长得一模一样。
 *  2. 响应里少了 `summary` 时这一页仍然是一页。类型上它是必填的,但类型说的是
 *     「后端应该给」,运行期拿到的是「后端这次给了什么」(接口降级、老版本
 *     后端、反向代理裁剪)。直读一层会在渲染中途抛 TypeError,把整页打掉,
 *     而这一页正是用来回答"现在哪些分组在被监控"的。
 *
 * 变异验证见文件末尾。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import zh from '@/i18n/qy/zh.json'

import type { QyAiScopeList } from '../types'

const domWindow = new Window({ height: 900, width: 1280 })
for (const key of [
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
  'ResizeObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
  'matchMedia',
  'DOMRect',
] as const) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}

const noopScroll = () => {}
Object.defineProperty(globalThis, 'scrollTo', {
  configurable: true,
  value: noopScroll,
})
Object.defineProperty(domWindow, 'scrollTo', {
  configurable: true,
  value: noopScroll,
})

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const {
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} = await import('@tanstack/react-router')

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zh as Record<string, string> } },
})

const { QyAdminViolationAiReview } = await import('../index')
const { qyKeys } = await import('../../../lib/query-keys')

const dict = zh as Record<string, string>

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const roots: { container: HTMLElement; root: { unmount: () => void } }[] = []
after(() => {
  for (const entry of roots) {
    entry.root.unmount()
    entry.container.remove()
  }
})

const scopes: QyAiScopeList = {
  items: [
    {
      id: 7,
      name: '自助注册分组',
      enabled: true,
      priority: 10,
      model_scope: '',
      group_scope: 'selfserve',
      group_scope_mode: 'include',
      pre_sample_rate_bps: 0,
      async_sample_rate_bps: 5000,
      category_id: 12,
      channel_id: 3,
      prompt: '',
      remark: '',
      created_at: 0,
      updated_at: 0,
    },
  ] as unknown as QyAiScopeList['items'],
  summary: [
    {
      id: 7,
      name: '自助注册分组',
      enabled: true,
      priority: 10,
      model_scope: '',
      group_scope: 'selfserve',
      group_scope_mode: 'include',
      pre_sample_rate_bps: 0,
      async_sample_rate_bps: 5000,
      prompt_source: 'custom',
      category_id: 12,
      channel_id: 3,
      channel_failover: false,
      group_unbound: false,
      shadowed: false,
    },
  ],
  max_scopes: 64,
  ai_enabled: true,
  effective_active: true,
  channels: [{ id: 3, name: '自建审核端点', enabled: true, model: 'm' }],
  active_scopes: [],
}

let activeQueryClient: InstanceType<typeof QueryClient> | null = null

const rootRoute = createRootRoute({ component: Outlet })
const pageRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/qy/admin/violation-ai-review',
  component: () => (
    <QueryClientProvider client={activeQueryClient!}>
      <QyAdminViolationAiReview />
    </QueryClientProvider>
  ),
})
const routeTree = rootRoute.addChildren([pageRoute])

/**
 * 设置卡的一份最小可用响应。
 *
 * 必须预置：整包跑时别的用例装的全局 fetch 桩会让这次查询落到一份不认识的
 * 对象上，而设置卡一旦在渲染中途抛异常，**整页**都会被路由那层的错误边界
 * 换掉 —— 连带作用域卡一起消失，于是本文件的断言会以"找不到汇总表"的形态
 * 失败，看起来像作用域卡坏了。这正是下面第四条用例要钉住的那件事。
 */
const settings = {
  setting: {
    enabled: false,
    pre_timeout_ms: 1500,
    async_timeout_ms: 8000,
    prompt: '',
    max_input_chars: 4000,
    third_party_notice_ack: false,
  },
  default_prompt: '默认提示词',
  prompt_source: 'default',
  categories: ['uncategorized', 'jailbreak'],
  category_block: '可用的 category 取值',
  prompt_preview: '默认提示词\n\n可用的 category 取值',
  category_details: [],
  prompt_categories: { unknown: [], missing: [] },
  effective: {
    active: false,
    channels: 0,
    max_pre_timeout: 5000,
    max_async_timeout: 30000,
  },
}

async function mountPage(payload: unknown, settingPayload: unknown = settings) {
  for (const entry of roots.splice(0)) {
    entry.root.unmount()
    entry.container.remove()
  }
  await act(async () => {})

  // 预置的数据一挂载就会被判为陈旧并触发真实 fetch，而这里没有网络；
  // 那次失败会把卡片换成错误态，表现为"单跑绿、合跑红"。
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchOnMount: false } },
  })
  queryClient.setQueryData(qyKeys.adminViolationAiScopes(), payload)
  queryClient.setQueryData(qyKeys.adminViolationAiSettings(), settingPayload)
  queryClient.setQueryData(qyKeys.adminViolationAiChannels(), { items: [] })
  queryClient.setQueryData(qyKeys.adminViolationAiStats(7), {
    total_calls: 0,
    total_tokens: 0,
    total_cost_usd: '0',
    violated_calls: 0,
    by_outcome: [],
    by_channel: [],
  })
  // qy 扩展开关。`QyPageBoundary` 在 `status === 'disabled'` 时整块换成
  // 「本站未启用该功能」的空态，卡片根本不渲染。
  queryClient.setQueryData(qyKeys.config(), { enabled: true })
  activeQueryClient = queryClient

  const router = createRouter({
    routeTree,
    history: createMemoryHistory({
      initialEntries: ['/qy/admin/violation-ai-review'],
    }),
  })
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(<RouterProvider router={router as never} />)
  })
  await act(async () => {})
  roots.push({ container, root })
  return container
}

/** 作用域汇总表：按「监控范围」这个只有它才有的表头锚定。 */
function scopeTable(container: HTMLElement): Element | null {
  for (const table of container.querySelectorAll('table')) {
    const heads = [...table.querySelectorAll('thead th')].map((n) =>
      (n.textContent ?? '').trim()
    )
    if (heads.includes(dict.qy_ai_scope_col_audience)) return table
  }
  return null
}

describe('AI 审核作用域卡', () => {
  test('汇总表接进了页面，表里只有真实存在的策略行', async () => {
    const container = await mountPage(scopes)
    const table = scopeTable(container)
    assert.ok(table, '页面上找不到作用域汇总表')
    const rows = [...table.querySelectorAll('tbody tr')]
    assert.equal(
      rows.length,
      1,
      '汇总行数不对：兜底档已经不存在，一行都不该多画出来'
    )
    assert.ok(
      (rows[0]?.textContent ?? '').includes('自助注册分组'),
      `第一行不是优先级最高的那一档：${rows[0]?.textContent}`
    )
  })

  /*
   * 每一行都有一个一键启停开关。
   *
   * 项目方要的是「增删改走弹窗、列表保留一键动作」。启停必须留在列表上：
   * 它是这一页唯一一个高频、单字段的动作，"打开弹窗 → 翻一个开关 → 保存"
   * 要三次点击，而它决定的是一批用户的请求内容会不会被发往第三方。
   */
  test('每一行都有启停开关，而且它反映的是这一档当前的状态', async () => {
    const container = await mountPage(scopes)
    const table = scopeTable(container)
    assert.ok(table)
    const row = table.querySelectorAll('tbody tr')[0]
    const toggle = row?.querySelector('[role="switch"]')
    assert.ok(toggle, '列表行上没有启停开关')
    assert.equal(
      toggle.getAttribute('aria-checked'),
      'true',
      '这一档是启用的，开关却显示成关着'
    )
  })

  /*
   * 「送到哪个渠道」这一列。
   *
   * 留空那一档写的是"按权重随机"而不是"默认渠道"：渠道表上没有 priority，
   * 两个渠道各 50% 权重时"默认渠道"这句话就是假的，而运营会按那句话去理解
   * 自己的数据流向。
   */
  test('汇总表摆出了「送到哪个渠道」，并把 id join 成了名字', async () => {
    const container = await mountPage(scopes)
    const table = scopeTable(container)
    assert.ok(table)
    const heads = [...table.querySelectorAll('thead th')].map((n) =>
      (n.textContent ?? '').trim()
    )
    assert.ok(heads.includes(dict.qy_ai_scope_col_channel), '缺「审核渠道」列')
    const first = table.querySelectorAll('tbody tr')[0]?.textContent ?? ''
    assert.ok(
      first.includes('自建审核端点'),
      `指定的渠道没有 join 成名字：${first}`
    )
  })

  /*
   * 指定的渠道被停用之后必须当场说出来。
   *
   * 这一档从此每一次都走「无可用渠道」并直接放行 —— 后端**绝不**回落到随机池
   * （回落会把用户内容发去运营明确没有选的端点）。也就是说它已经完全停止工作，
   * 而在没有这一格告警的列表上，它与一条正常策略长得一模一样。
   */
  test('指定的渠道被停用时，这一行被标成「渠道不可用」并给出一句话', async () => {
    const broken = {
      ...scopes,
      channels: [{ id: 3, name: '自建审核端点', enabled: false, model: 'm' }],
    }
    const text = (await mountPage(broken)).textContent ?? ''
    assert.ok(
      text.includes(dict.qy_ai_scope_kind_channel_down),
      '渠道被停用的那一档没有被标出来'
    )
    assert.ok(
      text.includes(dict.qy_ai_scope_channel_disabled),
      '没有说清后果 —— 只标一个状态词，运营不知道它意味着"这一档不再审核"'
    )
  })

  /*
   * 故障转移这一位必须出现在列表上,不能只藏在编辑弹窗里。
   *
   * 「指定了 A」与「指定了 A,但 A 不行时会发给别人」是两种不同的数据流向,
   * 而它们在这一格里只差一个 badge。不画的话,运营看着「审核渠道: 内部自建」
   * 得到的是一个已经不成立的预期 —— 而这一页上另一条更要紧的事实
   * (谁的内容被发往第三方)正是建立在那个预期上的。
   */
  test('开着故障转移的那一档,列表上写着它会退到别的渠道', async () => {
    const withFailover = {
      ...scopes,
      summary: [{ ...scopes.summary[0], channel_failover: true }],
    }
    const text = (await mountPage(withFailover)).textContent ?? ''
    assert.ok(
      text.includes(dict.qy_ai_scope_channel_failover_on),
      '指定渠道旁边没有标出故障转移 —— ' +
        '运营会以为内容只会去到他选的那一个端点'
    )
  })

  /*
   * 指定渠道坏了 + 开着故障转移:这一档**没有**失效,它退到了加权随机池。
   *
   * 报"渠道不可用"会把人引去修一个不影响判定结果的东西;真正该说的是另一件
   * 事(内容正在发往你没有指定的端点)。两句话的下一步动作完全不同。
   */
  test('指定的渠道停用 + 开着转移:说的是"落到池子上了",不是"这一档不再审核"', async () => {
    const broken = {
      ...scopes,
      summary: [{ ...scopes.summary[0], channel_failover: true }],
      channels: [{ id: 3, name: '自建审核端点', enabled: false, model: 'm' }],
    }
    const text = (await mountPage(broken)).textContent ?? ''
    assert.ok(
      !text.includes(dict.qy_ai_scope_kind_channel_down),
      '开了转移的档不该被标成「渠道不可用」—— 它照常在审'
    )
    assert.ok(
      text.includes(dict.qy_ai_scope_channel_broken_failover),
      '没有说清真正发生的事:每一次都直接落在加权随机池上'
    )
  })

  /*
   * 「问什么」与「记成哪一类」也必须出现在汇总表上。
   *
   * 两者与抽样率一样，没有任何用户可见的症状：一份写坏的作用域提示词、一档
   * 绑错了的违规类型，在列表上与正常的长得完全一样，而前者让这一档的判定口径
   * 整体偏掉、后者让类型计次（封号判据的一条线）落到别处。
   *
   * 类型名这里刻意断言成 `#12`：本测试没有网络，违规类型清单必然拉不到，
   * 而那时**绝不能**显示成空白 —— 空白读起来是"这一档没指定"，
   * 与"指定了但名字没查到"是两种完全不同的处置。
   */
  test('汇总表摆出了提示词档位与「记为类型」，类型清单拉不到时退回显示 id', async () => {
    const container = await mountPage(scopes)
    const table = scopeTable(container)
    assert.ok(table, '页面上找不到作用域汇总表')
    const heads = [...table.querySelectorAll('thead th')].map((n) =>
      (n.textContent ?? '').trim()
    )
    assert.ok(heads.includes(dict.qy_ai_scope_col_prompt), '缺「提示词」列')
    assert.ok(heads.includes(dict.qy_ai_scope_col_category), '缺「记为类型」列')

    const first = (table.querySelectorAll('tbody tr')[0]?.textContent ??
      '') as string
    assert.ok(
      first.includes(dict.qy_ai_scope_prompt_custom),
      `这一档写了自己的提示词，却没标出来：${first}`
    )
    assert.ok(first.includes('#12'), `类型名查不到时必须退回显示 id：${first}`)
  })

  /*
   * 新增 / 编辑走弹窗（项目方点名：「添加作用域策略，编辑作用域策略改成弹窗
   * 的方式」）。
   *
   * 断言落在**弹窗真的开出来了**，而不是"点击没报错"：改造前这里是一段内联
   * 表单，展开在表格下面把表格推出屏幕，"我正在改的是哪一行"完全看不见。
   * 一个只把 `editing` 置了值、却没有任何浮层的实现，在页面上与改造前一模一样。
   */
  test('点「新增」开出弹窗，而不是在表格下面展开一段内联表单', async () => {
    const container = await mountPage(scopes)
    assert.equal(
      document.body.querySelectorAll('[role="dialog"]').length,
      0,
      '还没点就已经有浮层了'
    )

    const addButton = [...container.querySelectorAll('button')].find((b) =>
      (b.textContent ?? '').includes(dict.qy_ai_scope_add)
    )
    assert.ok(addButton, '找不到「新增作用域策略」按钮')
    await act(async () => {
      addButton.dispatchEvent(new Event('click', { bubbles: true }))
    })

    const dialog = document.body.querySelector('[role="dialog"]')
    assert.ok(dialog, '点了新增却没有开出弹窗')
    const text = dialog.textContent ?? ''
    assert.ok(
      text.includes(dict.qy_ai_scope_create_title),
      `弹窗标题不对：${text.slice(0, 120)}`
    )
    // 「保存」必须在弹窗里够得着 —— 长表单里按钮跟着正文滚走时，
    // 用户会以为没有保存键。
    assert.ok(text.includes(dict.qy_ai_save), '弹窗里没有保存按钮')
    // 「送到哪个渠道」这一格也要在表单里，否则这一列只能看不能改。
    assert.ok(
      text.includes(dict.qy_ai_scope_f_channel),
      '弹窗表单里没有「审核渠道」那一格'
    )
  })

  test('一档都没在监控时，说的是"没有分组在被监控"而不是留白', async () => {
    const active = await mountPage(scopes)
    const activeText = active.textContent ?? ''

    // 上面那份数据里唯一启用的策略是 async 50%，它是 active。这里换一份全 0 的：
    // 两档都是 0 = 配了等于没配，而那正是最需要一句话的时候。
    // 注意先把上一次的文本抄下来 —— mountPage 会卸载并移除上一个容器。
    const idle = {
      ...scopes,
      summary: scopes.summary.map((row) => ({
        ...row,
        pre_sample_rate_bps: 0,
        async_sample_rate_bps: 0,
      })),
    }
    const idleText = (await mountPage(idle)).textContent ?? ''

    assert.ok(
      activeText.includes(
        dict.qy_ai_scope_monitored_count.replace('{{count}}', '1')
      ),
      `有档在监控时没写出档数：${activeText.slice(0, 200)}`
    )
    assert.ok(
      idleText.includes(dict.qy_ai_scope_none_title),
      '零档时没有"没有分组在被监控"的告警'
    )
    assert.ok(
      !idleText.includes(
        dict.qy_ai_scope_monitored_count.replace('{{count}}', '0')
      ),
      '零档时不该再写"有 0 档正在被监控" —— 那句话读起来像配好了'
    )
  })

  /*
   * 响应缺 `summary` 时这一页仍然是一页。
   *
   * 断言落在**这张卡的标题还在**,而不是"没抛异常":抛异常的表现就是整块被
   * 错误边界换掉,标题跟着消失。少一张表远好过少一整页 —— 白屏时连
   * 「作用域读不出来」这句话都显示不了。
   */
  test('响应缺 summary 时不白屏，卡片标题仍在', async () => {
    const container = await mountPage({
      items: [],
      max_scopes: 64,
      ai_enabled: true,
      effective_active: true,
      channels: [],
      active_scopes: [],
    })
    assert.ok(
      (container.textContent ?? '').includes(dict.qy_ai_scope_title),
      '缺 summary 时整张卡被打掉了'
    )
  })

  /*
   * 一张卡的降级响应不该打掉另一张卡。
   *
   * 这一页由四张卡拼成，它们同在一棵树上、共用**路由那一层**的错误边界：
   * 设置卡在渲染中途抛一次异常，作用域卡与渠道卡会一起消失，而运营看到的是
   * 「AI 审核这一页打不开了」—— 一个与真正的故障(设置接口降级)毫无相似之处
   * 的症状。所以爆炸半径必须被钉住，不能只钉每张卡自己。
   */
  test('设置卡的响应缺 setting 时，作用域卡仍然在页面上', async () => {
    const container = await mountPage(scopes, {
      default_prompt: '默认提示词',
      effective: { active: false, channels: 0 },
    })
    assert.ok(scopeTable(container), '设置卡降级把作用域卡一起带走了')
  })

  /*
   * 存量的「没绑分组」行必须在列表上说出来。
   *
   * 项目方要的是「强制绑定分组」,而那道闸只管**新的写入**:升级前建的空作用域
   * 策略照旧躺在库里,也照旧在按全站匹配 —— 它没有被自动停用,因为悄悄关掉一条
   * 正在生效的风控比留着它更危险。于是这一行成了全站唯一一个"抽样率作用在所有
   * 用户身上"的地方,而它在这张表上与一条只盯一个分组的策略长得完全一样。
   */
  test('存量未绑定分组的行被标出来，启用中的那句话点明它在按全站匹配', async () => {
    const legacy = {
      ...scopes,
      summary: [
        {
          ...scopes.summary[0]!,
          group_scope: '',
          group_unbound: true,
          enabled: true,
        },
      ],
    }
    const text = (await mountPage(legacy)).textContent ?? ''
    assert.ok(
      text.includes(dict.qy_ai_scope_unbound_active),
      `启用中的未绑定行没有被标出来：${text.slice(0, 200)}`
    )
  })

  test('停用的未绑定行说的是"补上分组才能启用"，不是"正在全站匹配"', async () => {
    const legacy = {
      ...scopes,
      summary: [
        {
          ...scopes.summary[0]!,
          group_scope: '',
          group_unbound: true,
          enabled: false,
        },
      ],
    }
    const text = (await mountPage(legacy)).textContent ?? ''
    assert.ok(
      text.includes(dict.qy_ai_scope_unbound_idle),
      '停用的未绑定行没有给出"补齐分组才能启用"的说明'
    )
    assert.ok(
      !text.includes(dict.qy_ai_scope_unbound_active),
      '停用的档一个请求都收不到，说它"正在按全站匹配"是假的'
    )
  })

  /*
   * 分组这一格是必填,而且拦在**保存键**上。
   *
   * 新建出来的草稿分组就是空的,所以弹窗一开就该是这个状态。让它可保存、再由
   * 后端回一句 400 也"能用",但那句话出现在弹窗外面的 toast 里 —— 人正看着表单,
   * 提示却在别处,而且他刚刚已经把十来格填完了。
   */
  test('新建弹窗一开:分组为空 → 保存键是灰的,并且当场说明为什么必填', async () => {
    const container = await mountPage(scopes)
    const addButton = [...container.querySelectorAll('button')].find((b) =>
      (b.textContent ?? '').includes(dict.qy_ai_scope_add)
    )
    assert.ok(addButton, '找不到「新增作用域策略」按钮')
    await act(async () => {
      addButton.dispatchEvent(new Event('click', { bubbles: true }))
    })

    const dialog = document.body.querySelector('[role="dialog"]')
    assert.ok(dialog, '点了新增却没有开出弹窗')
    const save = [...dialog.querySelectorAll('button')].find((b) =>
      (b.textContent ?? '').includes(dict.qy_ai_save)
    )
    assert.ok(save, '弹窗里没有保存按钮')
    assert.ok(
      save.hasAttribute('disabled'),
      '分组还没填,保存键却是可点的 —— 这一条只能等后端 400,而那句话在弹窗外面'
    )
    assert.ok(
      (dialog.textContent ?? '').includes(dict.qy_ai_scope_group_required),
      '没有说明为什么必填 —— 运营的第一反应会是"以前留空就行,现在坏了"'
    )
  })
})

/*
 * ── 变异验证（逐条改坏 `index.tsx` 再跑本文件，改完即还原并校验 sha256；
 *    括号里是实测结果）──
 *
 *  S1 页面里删掉 `<AiScopeCard />`                    → 4 条全红（卡片没接进页面）
 *  S2 `monitoredAll` 改回直读 `data.summary`          → 「缺 summary 时不白屏」红
 *     报错原文：`undefined is not an object (evaluating 'data.summary.map')`。
 *  S3 汇总表改成按 `id` 升序渲染（兜底档不再垫底）      → 「末行是兜底档」红
 *  S4 零档时不渲染 `qy_ai_scope_none_title`           → 「一档都没在监控时…」红
 *  S5 设置卡的 `data?.setting ?` 改回 `data ?`        → 「设置卡的响应缺 setting 时…」红
 *     报错原文：`undefined is not an object (evaluating 'data.setting.enabled')`，
 *     异常发生在 <AiSettingCard>，而**红的是作用域卡的用例** —— 那正是爆炸半径
 *     这件事的形状：坏的是别人，消失的是你。
 *  S6 行里那段 `row.group_unbound && …` 删掉          → 「存量未绑定分组的行…」两条红
 *  S7 未绑定行的两句话不分启停(恒用 active 那一句)   → 「停用的未绑定行…」红
 *  S8 保存键的 `bindingError !== null` 去掉           → 「新建弹窗一开…」红
 *     (保存键可点,而那一档存下去之后永远开不起来)
 */
