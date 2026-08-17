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
 * 「用户佣金」的交互判据 —— 真 DOM、真文案、真 axios（假 adapter）。
 *
 * # 守什么
 *
 * 项目方要的三件事里有两件只在**点下去之后**才成立，源码级断言看不到：
 *
 *   1. 「编辑/移除/添加这个用户的佣金绑定关系」—— 可用的动作必须**跟着这一行
 *      有没有上线走**。给一个点了必然 400 的选项，正是他今天在「拉黑」上撞到的
 *      同一种缺陷（`api_admin.go` 对没有关系的行直接拒）。
 *   2. 「必须说清楚已产生的佣金怎么办」—— 这句话要**出现在确认框里**，
 *      而不是写在某个注释或文档里。
 *
 * 所以下面断言的是"文本/选项出现在 DOM 里"和"按钮此刻能不能按"，不是
 * "组件收到了什么 props"。文案走真实的 `src/i18n/qy/zh.json`：键写错时
 * i18next 原样吐出键名，中文断言当场变红 —— 这同时把「新增分支忘了补文案」钉住。
 *
 * # 请求断言
 *
 * 换绑必须打**一条** `relations/rebind`，而不是 `unbind` 紧接着 `bind`。
 * 后者第二步失败时用户会停在"没有上线"的中间态，而屏幕上只有一句"操作失败"。
 * 这条只有真的发出请求才验得到，源码里 grep 不出"发了几个"。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { after, beforeEach, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

import type { QyCommissionUser } from '../types'

const here = dirname(fileURLToPath(import.meta.url))
const srcDir = join(here, '..', '..', '..', '..', '..')

const domWindow = new Window({ height: 900, width: 1280 })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'HTMLInputElement',
  'HTMLSelectElement',
  'HTMLTextAreaElement',
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

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')

const zhBundle = JSON.parse(
  readFileSync(join(srcDir, 'i18n', 'qy', 'zh.json'), 'utf8')
) as Record<string, string>

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zhBundle } },
})

const { api } = await import('@/lib/api')
const { formatQyQuotaLedger } = await import('../../../lib/format')
const { ManageRelationDialog } =
  await import('../components/manage-relation-dialog')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/** 每次请求记一笔，测试据此断言"打了哪条路由、打了几条"。 */
let sent: { url: string; body: unknown }[] = []
api.defaults.adapter = async (config) => {
  sent.push({
    url: String(config.url),
    body:
      typeof config.data === 'string'
        ? (JSON.parse(config.data) as unknown)
        : config.data,
  })
  return {
    data: { success: true, data: { kept_commission_quota: 4200 } },
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  }
}

const mounted: {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}[] = []

async function unmountAll() {
  for (;;) {
    const entry = mounted.pop()
    if (entry == null) return
    await act(async () => entry.root.unmount())
    entry.container.remove()
  }
}

after(unmountAll)
beforeEach(() => {
  sent = []
})

/** 一行"用户佣金"。默认没有上线、拉了 3 个人、账上有钱。 */
function userRow(patch: Partial<QyCommissionUser> = {}): QyCommissionUser {
  return {
    user_id: 1301,
    username: 'qy-lot-admin',
    user_resolved: true,
    display_name: '',
    email: 'qy@example.com',
    user_group: 'default',

    inviter_id: 0,
    inviter_username: '',
    inviter_resolved: false,
    inviter_blocked: false,
    inviter_commission_quota: 0,

    invitee_count: 3,
    blocked_invitee_count: 0,
    has_balance_row: true,

    available_quota: 137_200,
    frozen_quota: 0,
    withdrawn_quota: 0,
    total_earned_quota: 137_200,
    total_clawback_quota: 0,
    derived_available_quota: 137_200,
    ledger_drift: 0,
    unsettled_amount: '0',
    available_fiat: '0',
    debt_blocked: false,
    last_settled_at: 0,
    updated_at: 0,
    ...patch,
  }
}

async function mountDialog(user: QyCommissionUser) {
  await unmountAll()
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  })
  await act(async () =>
    root.render(
      <QueryClientProvider client={client}>
        <ManageRelationDialog user={user} onClose={() => {}} />
      </QueryClientProvider>
    )
  )
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  mounted.push({ container, root })
}

function screenText(): string {
  return document.body.textContent ?? ''
}

/** 取一句真实文案。键不存在时当场失败，不允许回落成空串（那会让断言恒真）。 */
function copy(key: string): string {
  const value = zhBundle[key]
  assert.ok(value != null && value !== '', `文案键 ${key} 没有登记进 zh.json`)
  return value
}

function actionOptions(): string[] {
  return [...document.querySelectorAll('select option')].map(
    (node) => node.textContent ?? ''
  )
}

/**
 * 提交按钮 —— 按**文案**找，不按位置找。
 *
 * 位置（`buttons.at(-1)`）会摸到浮层外壳自带的关闭按钮，那个永远是 enabled，
 * 于是"事由没填也能提交"这条断言会假绿。按钮上的字正是当前选中的那个动作
 * （「指定上线」/「更换上线」…），这也是运营点下去之前唯一能看到的确认。
 */
function submitButton(): HTMLButtonElement {
  const select = document.querySelector('select') as HTMLSelectElement | null
  assert.ok(select != null, '弹窗里没有动作下拉')
  const label = copy(`qy_cu_rel_act_${select.value}`)
  const submit = [...document.querySelectorAll('button')].find(
    (node) => (node.textContent ?? '').trim() === label
  )
  assert.ok(submit != null, `找不到写着「${label}」的提交按钮`)
  return submit as unknown as HTMLButtonElement
}

async function fill(selector: string, value: string) {
  const node = document.querySelector(selector) as HTMLInputElement | null
  assert.ok(node != null, `找不到 ${selector}`)
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(
      node.constructor.prototype as object,
      'value'
    )?.set
    setter?.call(node, value)
    node.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

/* ── 1. 可用动作跟着"这一行有没有上线"走 ──────────────────────────── */

describe('关系动作的可用性由这一行的数据决定', () => {
  test('没有上线时：只能「指定上线」与「添加下线」', async () => {
    await mountDialog(userRow({ inviter_id: 0 }))
    const options = actionOptions()
    assert.deepEqual(options, [
      copy('qy_cu_rel_act_set_inviter'),
      copy('qy_cu_rel_act_add_invitee'),
    ])
    // 「更换/移除」在后端一定是 qy_rel_not_bound，给出来就是一个点了必报错的选项
    // —— 与项目方今天在「拉黑」上撞到的是同一种缺陷。
    assert.ok(!options.includes(copy('qy_cu_rel_act_replace_inviter')))
    assert.ok(!options.includes(copy('qy_cu_rel_act_remove_inviter')))
    // 当前上线那一行必须明说"没有"，而不是渲染成 `#0`。
    assert.ok(screenText().includes(copy('qy_cu_no_inviter')))
  })

  test('已有上线时：只能「更换/移除上线」与「添加下线」', async () => {
    await mountDialog(
      userRow({
        inviter_id: 1302,
        inviter_username: 'qy-upline',
        inviter_resolved: true,
      })
    )
    const options = actionOptions()
    assert.deepEqual(options, [
      copy('qy_cu_rel_act_replace_inviter'),
      copy('qy_cu_rel_act_remove_inviter'),
      copy('qy_cu_rel_act_add_invitee'),
    ])
    assert.ok(!options.includes(copy('qy_cu_rel_act_set_inviter')))
    // 当前上线要指名道姓：改关系之前必须看得见"现在挂在谁名下"。
    assert.ok(screenText().includes('qy-upline'))
    assert.ok(screenText().includes('#1302'))
  })
})

/* ── 2. 确认框必须说清"已产生的佣金怎么办" ────────────────────────── */

describe('确认框把资金语义直接说出来', () => {
  test('四个动作共用的那一句在屏幕上', async () => {
    await mountDialog(userRow({ total_earned_quota: 137_200 }))
    const text = screenText()
    const semantics = copy('qy_cu_rel_semantics')

    assert.ok(
      text.includes(semantics),
      '「已产生的佣金全部保留、从此不再产生新的」没有出现在确认框里'
    )
    assert.ok(semantics.includes('保留'))
    assert.ok(
      semantics.includes('冲正'),
      '没指出"要收回已发放的佣金请走冲正"，运营会以为改绑定就能把钱收回来'
    )
  })

  /**
   * 确认框上那个金额必须是「**当前上线**从这个人身上挣到的」，
   * 而不是「这个人从**他自己下线**身上挣到的」。
   *
   * 两者是方向相反的两个数。线上实测的形状：397 号自己没有下线
   * （`total_earned_quota` = 0），而他上线 391 从他身上已挣到 13517 —— 渲染错
   * 那一个，确认框写「保留 0」，点下去之后的成功提示写「保留 13517」。
   * 同一次操作、同一个标签、两个数差 13517，而这是一个改钱的页面。
   *
   * 这里刻意让两个字段取**互相不可能混淆**的值：混用哪一个都会当场变红。
   */
  test('保留金额念的是"上线从他身上挣的"，不是"他从下线身上挣的"', async () => {
    await mountDialog(
      userRow({
        inviter_id: 391,
        inviter_username: 'SCPO5',
        inviter_resolved: true,
        // 他自己一个下线都没有，这个数不该出现在确认框上。
        total_earned_quota: 137_200,
        // 他上线从他身上挣到的 —— 这才是解绑/换绑后会留下的那笔。
        inviter_commission_quota: 13_517,
      })
    )
    const text = screenText()

    assert.ok(
      text.includes(copy('qy_cu_rel_kept_from_inviter')),
      '有上线时确认框里必须有"留在当前上线名下的历史佣金"这一行'
    )
    // 金额一律走站内余额口径，不是裸 quota 整数：运营要拿它和用户在钱包页
    // 看到的数字对话。
    assert.ok(
      !text.includes('13517') && !text.includes('137200'),
      '金额渲染成了裸 quota 整数，与用户在钱包页看到的口径对不上'
    )
    assert.ok(
      text.includes(formatQyQuotaLedger(13_517)),
      '确认框念的不是"当前上线从他身上挣到的"那个数'
    )
    assert.ok(
      !text.includes(formatQyQuotaLedger(137_200)),
      '确认框念成了 total_earned_quota —— 那是他从自己下线身上挣的，方向反了'
    )
  })

  /**
   * 「指定上线」与「添加下线」建立的是一条**全新**关系，没有任何既有佣金可言。
   * 此时挂一个金额出来，运营会以为这次操作会动到那笔钱。
   */
  test('建立新关系的两档不显示任何保留金额', async () => {
    await mountDialog(userRow({ inviter_id: 0, total_earned_quota: 137_200 }))
    const text = screenText()
    assert.ok(
      !text.includes(copy('qy_cu_rel_kept_from_inviter')),
      '没有上线时不该有"留在当前上线名下的历史佣金"这一行'
    )
    assert.ok(
      !text.includes(formatQyQuotaLedger(137_200)),
      '建立新关系不涉及任何既有佣金，屏幕上不该出现他的累计 earned'
    )
  })
})

/* ── 3. 提交闸门：事由、自邀请、换成同一个人 ──────────────────────── */

describe('提交按钮在什么情况下才允许按', () => {
  test('事由不足 4 个字时按不动', async () => {
    await mountDialog(userRow({ inviter_id: 0 }))
    await fill('input[inputmode="numeric"]', '1400')
    assert.equal(submitButton().disabled, true, '没填事由就能提交')

    await fill('textarea', '手工绑定')
    assert.equal(submitButton().disabled, false, '填够事由之后仍然按不动')
  })

  test('自己邀请自己：当场拒绝并说明原因', async () => {
    await mountDialog(userRow({ inviter_id: 0 }))
    await fill('input[inputmode="numeric"]', '1301')
    await fill('textarea', '这是一次测试绑定')

    assert.equal(submitButton().disabled, true, '自邀请仍然可以提交')
    assert.ok(
      screenText().includes(copy('qy_err_rel_self_invite')),
      '挡住了但没说为什么 —— 运营会以为页面坏了'
    )
    assert.equal(sent.length, 0, '前端就该挡住的请求被发了出去')
  })

  test('换成他现在这个上线：当场拒绝，不白跑一趟后端', async () => {
    await mountDialog(
      userRow({
        inviter_id: 1302,
        inviter_username: 'qy-upline',
        inviter_resolved: true,
      })
    )
    await fill('input[inputmode="numeric"]', '1302')
    await fill('textarea', '这是一次测试换绑')

    assert.equal(submitButton().disabled, true)
    assert.ok(screenText().includes(copy('qy_err_rel_same_inviter')))
    assert.equal(sent.length, 0)
  })
})

/* ── 4. 换绑打的是原子端点，不是 unbind + bind 两步 ───────────────── */

describe('换绑走后端的原子端点', () => {
  test('提交后只发出一条 rebind 请求', async () => {
    await mountDialog(
      userRow({
        inviter_id: 1302,
        inviter_username: 'qy-upline',
        inviter_resolved: true,
      })
    )
    await fill('input[inputmode="numeric"]', '1400')
    await fill('textarea', '这是一次测试换绑')
    await act(async () => {
      submitButton().click()
    })
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0))
    })

    assert.equal(
      sent.length,
      1,
      `换绑发出了 ${sent.length} 条请求：两条就意味着它又拼回了 unbind + bind，第二条失败时用户会停在"没有上线"的中间态`
    )
    assert.equal(sent[0].url, '/api/qy/admin/commission/relations/rebind')
    assert.deepEqual(sent[0].body, {
      invitee_id: 1301,
      inviter_id: 1400,
      reason: '这是一次测试换绑',
    })
  })

  test('「添加下线」把方向反过来：这个人是邀请人', async () => {
    await mountDialog(userRow({ inviter_id: 0 }))
    const select = document.querySelector('select') as HTMLSelectElement | null
    assert.ok(select != null)
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(
        select.constructor.prototype as object,
        'value'
      )?.set
      setter?.call(select, 'add_invitee')
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    await fill('input[inputmode="numeric"]', '1400')
    await fill('textarea', '这是一次测试加下线')
    await act(async () => {
      submitButton().click()
    })
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0))
    })

    assert.equal(sent.length, 1)
    assert.equal(sent[0].url, '/api/qy/admin/commission/relations/bind')
    assert.deepEqual(
      sent[0].body,
      { invitee_id: 1400, inviter_id: 1301, reason: '这是一次测试加下线' },
      '方向搞反了：那会把这个人变成对方的下线，佣金从此流向反方向'
    )
  })
})
