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
 * 试跑面板要**自己解释「为什么只有这几格」**。
 *
 * # 守什么
 *
 * 上一轮做对了「字段跟着规则走」：一条 prompt 阶段的关键词规则只问「请求上下文」，
 * 一条错误码规则只问错误码。判据是对的，实测四种组合全部吻合。
 *
 * 但项目方看到的是另一回事。库里 34 条规则有 30 条落在 prompt 阶段，随手点开一条，
 * 面板上就只剩「请求上下文」孤零零一格 —— 于是结论是「上游返回文本、返回代码、
 * 状态码这几样根本没做」。它们一直都在，只是**这条规则不读**，而面板把这件事
 * 静默省略了。
 *
 * 所以这里钉住的不是「问哪几格」（那由 rule-test-inputs.test.ts 守），
 * 而是**面板有没有把判据本身摆出来**：
 *
 *   1. 缺席的维度**逐条列出来**，并各自带一条能读懂的理由；
 *   2. 理由由「阶段」还是「匹配方式」造成，必须点名正确的那一个 ——
 *      指错了会把管理员送去改一个本来就对的下拉框；
 *   3. 那几行**只读**：一个输入控件都不许有。填了不生效比不显示更糟，
 *      而那正是上一轮修掉的毛病；
 *   4. 改判据的路径就在试跑区里，改完**字段与说明当场跟着变**；
 *   5. `ai_review` 那一档同样说得清：它问的是模型给出的结论，不是上下文。
 *
 * 文案走真实的 `src/i18n/qy/zh.json`：键写错时 i18next 原样吐键名，
 * 下面的中文断言当场变红。期望值一律在测试里独立拼出来，不从产品代码回读。
 *
 * 变异实验见文件末尾的记录。
 */
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { after, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import { readJsxTree } from '../../../__tests__/jsx-tree'
import {
  QY_VIOLATION_TEST_DIMENSIONS,
  qyRuleTestAbsentDimensions,
  qyRuleTestInputs,
} from '../lib/rule-test'
import type {
  QyViolationMatchType,
  QyViolationPhase,
  QyViolationTestInput,
} from '../types'

const here = dirname(fileURLToPath(import.meta.url))
const componentsDir = join(here, '..', 'components')

/** 独立拼出期望文案：产品代码怎么插值，这里不看，只按 i18n 原文自己替换。 */
function zhText(key: string, vars: Record<string, string> = {}): string {
  let out = (zh as Record<string, string>)[key]
  assert.ok(out != null, `zh.json 缺少 ${key}`)
  for (const [name, value] of Object.entries(vars)) {
    out = out.split(`{{${name}}}`).join(value)
  }
  return out
}

/* ── 1. 缺席维度与理由 ──────────────────────────────────────────────── */

describe('每一个读不到的维度都有一条指对了地方的理由', () => {
  type Case = {
    name: string
    phase: QyViolationPhase
    matchType: QyViolationMatchType
    want: [QyViolationTestInput, string][]
  }

  const cases: Case[] = [
    {
      // 项目方截图里的那一屏：30/34 条规则长这样。
      name: 'prompt 关键词：五样上游维度全部缺席，怪的是阶段；其余怪匹配方式',
      phase: 'prompt',
      matchType: 'keyword',
      want: [
        ['upstream_text', 'phase_no_upstream'],
        ['reject_reason', 'phase_no_upstream'],
        ['status_code', 'phase_no_upstream'],
        ['error_code', 'match_not_error_code'],
        ['rate_count', 'match_not_rate'],
        ['ai_verdict', 'match_not_ai'],
        ['ai_category', 'match_not_ai'],
        ['ai_confidence', 'match_not_ai'],
      ],
    },
    {
      name: '上游阶段的关键词：请求上下文缺席，怪的是阶段而不是匹配方式',
      phase: 'upstream_err',
      matchType: 'keyword',
      want: [
        ['request_text', 'phase_not_prompt'],
        ['error_code', 'match_not_error_code'],
        ['rate_count', 'match_not_rate'],
        ['ai_verdict', 'match_not_ai'],
        ['ai_category', 'match_not_ai'],
        ['ai_confidence', 'match_not_ai'],
      ],
    },
    {
      // 「上游文本」这个匹配方式本身也是**文本判据**，只是名字里带了上游。挂在
      // prompt 阶段时它扫的仍然是请求上下文，上游那两段读不到该怪阶段。把它
      // 漏出文本判据清单，说明就会变成「匹配方式是『上游文本』，判据一个字节的
      // 文本都不读」—— 一句自相矛盾、还把管理员支去改匹配方式的话。
      name: 'prompt 阶段的上游文本判据：缺席怪阶段，不怪这个带「文本」二字的匹配方式',
      phase: 'prompt',
      matchType: 'upstream_text',
      want: [
        ['upstream_text', 'phase_no_upstream'],
        ['reject_reason', 'phase_no_upstream'],
        ['status_code', 'phase_no_upstream'],
        ['error_code', 'match_not_error_code'],
        ['rate_count', 'match_not_rate'],
        ['ai_verdict', 'match_not_ai'],
        ['ai_category', 'match_not_ai'],
        ['ai_confidence', 'match_not_ai'],
      ],
    },
    {
      name: '上游文本判据挂在上游阶段：请求上下文缺席同样怪阶段',
      phase: 'upstream_err',
      matchType: 'upstream_text',
      want: [
        ['request_text', 'phase_not_prompt'],
        ['error_code', 'match_not_error_code'],
        ['rate_count', 'match_not_rate'],
        ['ai_verdict', 'match_not_ai'],
        ['ai_category', 'match_not_ai'],
        ['ai_confidence', 'match_not_ai'],
      ],
    },
    {
      name: '错误码规则：三段文本一起缺席，怪的是匹配方式 —— 改阶段没用',
      phase: 'upstream_err',
      matchType: 'error_code',
      want: [
        ['request_text', 'match_not_text'],
        ['upstream_text', 'match_not_text'],
        ['reject_reason', 'match_not_text'],
        ['rate_count', 'match_not_rate'],
        ['ai_verdict', 'match_not_ai'],
        ['ai_category', 'match_not_ai'],
        ['ai_confidence', 'match_not_ai'],
      ],
    },
    {
      name: '频率规则：一段文本都不读，且上游状态码也读不到',
      phase: 'prompt',
      matchType: 'request_rate',
      want: [
        ['request_text', 'match_not_text'],
        ['upstream_text', 'match_not_text'],
        ['reject_reason', 'match_not_text'],
        ['status_code', 'phase_no_upstream'],
        ['error_code', 'match_not_error_code'],
        ['ai_verdict', 'match_not_ai'],
        ['ai_category', 'match_not_ai'],
        ['ai_confidence', 'match_not_ai'],
      ],
    },
    {
      name: '状态码规则：状态码是唯一在场的，其余文本怪匹配方式',
      phase: 'upstream_err',
      matchType: 'status_code',
      want: [
        ['request_text', 'match_not_text'],
        ['upstream_text', 'match_not_text'],
        ['reject_reason', 'match_not_text'],
        ['error_code', 'match_not_error_code'],
        ['rate_count', 'match_not_rate'],
        ['ai_verdict', 'match_not_ai'],
        ['ai_category', 'match_not_ai'],
        ['ai_confidence', 'match_not_ai'],
      ],
    },
    {
      name: '转发后异步的 AI 审核：状态码缺席的理由是异步，不是「还没发上游」',
      phase: 'post_async',
      matchType: 'ai_review',
      want: [
        ['request_text', 'match_not_text'],
        ['upstream_text', 'match_not_text'],
        ['reject_reason', 'match_not_text'],
        ['status_code', 'phase_async'],
        ['error_code', 'match_not_error_code'],
        ['rate_count', 'match_not_rate'],
      ],
    },
  ]

  for (const tc of cases) {
    test(tc.name, () => {
      assert.deepEqual(
        qyRuleTestAbsentDimensions(tc.phase, tc.matchType).map((row) => [
          row.id,
          row.reason,
        ]),
        tc.want
      )
    })
  }

  test('在场 + 缺席 == 全部维度，任何一格都不会凭空消失', () => {
    // 这是整块面板的立身之本：静默省略正是这次要修的缺陷。少一个维度既不在
    // 输入里、也不在说明里，界面上就又变回「这东西根本没做」。
    for (const phase of [
      'prompt',
      'upstream_err',
      'reject_reason',
      'post_async',
    ] as QyViolationPhase[]) {
      for (const matchType of [
        'keyword',
        'regex',
        'upstream_text',
        'error_code',
        'status_code',
        'request_rate',
        'ai_review',
      ] as QyViolationMatchType[]) {
        const asked: QyViolationTestInput[] = qyRuleTestInputs(
          phase,
          matchType
        ).filter((id) => id !== 'model' && id !== 'group')
        const absent = qyRuleTestAbsentDimensions(phase, matchType).map(
          (row) => row.id
        )
        assert.deepEqual(
          [...asked, ...absent].sort(),
          [...QY_VIOLATION_TEST_DIMENSIONS].sort(),
          `${phase} × ${matchType}：在场与缺席拼不回完整清单`
        )
        // 同一格既被问、又被说成读不到 —— 两套判据漂移时最坏的表现。
        for (const id of absent) {
          assert.ok(
            !asked.includes(id),
            `${phase} × ${matchType}：${id} 两边都在`
          )
        }
      }
    }
  })
})

/* ── 2. 文案 ────────────────────────────────────────────────────────── */

describe('说明文案齐备且插值口径一致', () => {
  test('每一种缺席理由中英各有一条文案', () => {
    const reasons = new Set<string>()
    for (const phase of [
      'prompt',
      'upstream_err',
      'reject_reason',
      'post_async',
    ] as QyViolationPhase[]) {
      for (const matchType of [
        'keyword',
        'regex',
        'upstream_text',
        'error_code',
        'status_code',
        'request_rate',
        'ai_review',
      ] as QyViolationMatchType[]) {
        for (const row of qyRuleTestAbsentDimensions(phase, matchType)) {
          reasons.add(row.reason)
        }
      }
    }
    // 七种理由一个都不能少：漏一条，界面上那一行会渲染成键名本身。
    assert.deepEqual([...reasons].sort(), [
      'match_not_ai',
      'match_not_error_code',
      'match_not_rate',
      'match_not_text',
      'phase_async',
      'phase_no_upstream',
      'phase_not_prompt',
    ])
    for (const reason of reasons) {
      const key = `qy_vio_test_absent_${reason}`
      assert.ok(key in zh, `zh.json 缺少 ${key}`)
      assert.ok(key in en, `en.json 缺少 ${key}`)
    }
    for (const key of [
      'qy_vio_test_criteria_now',
      'qy_vio_test_criteria_desc',
      'qy_vio_test_absent_summary',
      'qy_vio_test_absent_readonly',
      'qy_vio_test_ai_desc',
    ]) {
      assert.ok(key in zh, `zh.json 缺少 ${key}`)
      assert.ok(key in en, `en.json 缺少 ${key}`)
    }
  })

  test('怪阶段的文案念阶段，怪匹配方式的文案念匹配方式', () => {
    // 指错地方比不指更糟：一条「匹配方式是错误码所以不读文本」的规则，
    // 说明里如果念的是阶段，管理员会去把阶段改来改去，而那一格永远不会出现。
    const bundles = [zh, en] as Record<string, string>[]
    for (const bundle of bundles) {
      for (const reason of [
        'phase_async',
        'phase_no_upstream',
        'phase_not_prompt',
      ]) {
        const text = bundle[`qy_vio_test_absent_${reason}`]
        assert.ok(text.includes('{{phase}}'), `${reason} 没有念出阶段`)
        assert.ok(!text.includes('{{match}}'), `${reason} 不该念匹配方式`)
      }
      for (const reason of [
        'match_not_ai',
        'match_not_error_code',
        'match_not_rate',
        'match_not_text',
      ]) {
        const text = bundle[`qy_vio_test_absent_${reason}`]
        assert.ok(text.includes('{{match}}'), `${reason} 没有念出匹配方式`)
        assert.ok(!text.includes('{{phase}}'), `${reason} 不该念阶段`)
      }
    }
  })
})

/* ── 3. 面板形状（AST）─────────────────────────────────────────────── */

describe('缺席维度那一块是只读的，判据选择器就在面板里', () => {
  const tree = readJsxTree(join(componentsDir, 'rule-tester.tsx'))

  test('说明区里一个输入控件都没有', () => {
    // `<details>` 在这个文件里只有一处，就是缺席维度那一块。任何 Input /
    // Textarea / Select / Button 落进它的祖先链，就等于把「读不到的维度」
    // 做成了能填的格子 —— 填了不生效，比不显示更糟。
    assert.equal(
      tree.occurrences('details').length,
      1,
      '这个文件里出现了不止一处 details，下面的祖先链判定会失准'
    )
    for (const control of [
      'Input',
      'Textarea',
      'Select',
      'SelectTrigger',
      'SelectItem',
      'Button',
      'Switch',
      'input',
      'textarea',
      'select',
    ]) {
      for (const ancestors of tree.occurrences(control)) {
        assert.ok(
          !ancestors.includes('details'),
          `「读不到的维度」那一块里出现了可填的 ${control}`
        )
      }
    }
  })

  test('两个判据选择器写回的是表单字段，不是面板自己的副本', () => {
    // 面板自己 useState 存一份阶段/匹配方式，就变成了「试跑的是 A、保存的是 B」——
    // 一个比不给试跑更危险的安全假象。
    assert.ok(
      tree.source.includes('props.onPhaseChange('),
      '试跑区里的阶段选择器没有写回表单'
    )
    assert.ok(
      tree.source.includes('props.onMatchTypeChange('),
      '试跑区里的匹配方式选择器没有写回表单'
    )
    assert.ok(
      !/useState[^\n]*\bphase\b/i.test(tree.source),
      '面板自己存了一份阶段：试跑的规则会与要保存的规则漂移'
    )
  })

  test('规则表单把这两个回调接到了同一份 form 上', () => {
    const sheet = readJsxTree(join(componentsDir, 'rule-form-sheet.tsx'))
    for (const wiring of [
      "form.setValue('phase', value",
      "form.setValue('match_type', value",
    ]) {
      assert.ok(
        sheet.source.includes(wiring),
        `规则表单没有把试跑区的判据改动写回 ${wiring}`
      )
    }
    // 面板必须真的挂在表单里，而不是只写了 import。
    assert.equal(
      sheet.occurrences('QyRuleTester').length,
      1,
      '试跑面板没有挂进规则表单'
    )
  })
})

/* ── 4. 交互：改判据 → 字段与说明当场跟着变 ────────────────────────── */

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

const { act, useState } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zh as Record<string, string> } },
})

const { qyEmptyViolationRule } = await import('../lib/rule-form')
const { QyRuleTester } = await import('../components/rule-tester')

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

/**
 * 试跑面板的探针。判据由**面板自己的选择器**改 —— 探针只提供表单那一侧的
 * 落点（`onPhaseChange` / `onMatchTypeChange`），与 `rule-form-sheet` 的接线同形。
 */
let switchTo:
  | ((
      next: Partial<{
        matchType: QyViolationMatchType
        phase: QyViolationPhase
      }>
    ) => void)
  | null = null

function TesterProbe(props: {
  phase: QyViolationPhase
  matchType: QyViolationMatchType
}) {
  const [criteria, setCriteria] = useState({
    matchType: props.matchType,
    phase: props.phase,
  })
  switchTo = (next) => setCriteria((prev) => ({ ...prev, ...next }))
  return (
    <QyRuleTester
      getValues={(() => qyEmptyViolationRule()) as never}
      phase={criteria.phase}
      matchType={criteria.matchType}
      onPhaseChange={(phase) => setCriteria((prev) => ({ ...prev, phase }))}
      onMatchTypeChange={(matchType) =>
        setCriteria((prev) => ({ ...prev, matchType }))
      }
    />
  )
}

async function mount(phase: QyViolationPhase, matchType: QyViolationMatchType) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <TesterProbe phase={phase} matchType={matchType} />
      </QueryClientProvider>
    )
  })
  await act(async () => {})
  roots.push({ container, root })
  return container
}

function placeholders(container: HTMLElement): string[] {
  return [...container.querySelectorAll('input,textarea')].map(
    (node) => node.getAttribute('placeholder') ?? ''
  )
}

function absentBlock(container: HTMLElement): HTMLElement {
  const block = container.querySelector('details')
  assert.ok(block != null, '面板里没有「读不到的维度」那一块')
  return block as HTMLElement
}

describe('试跑面板自己说清楚「为什么只有这几格」', () => {
  test('prompt 关键词规则：判据摆在面板里，缺席的四样维度点名在案', async () => {
    const container = await mount('prompt', 'keyword')
    const text = container.textContent ?? ''

    // 1. 判据本身直接写在试跑区，不必回到表单上方去核对。
    assert.ok(
      text.includes(
        zhText('qy_vio_test_criteria_now', {
          match: zhText('qy_vio_match_keyword'),
          phase: zhText('qy_vio_phase_prompt'),
        })
      ),
      '试跑区没有写出这条规则此刻的阶段与匹配方式'
    )
    // 2. 改判据的路径就在手边：两个**真能点**的选择器。存在但藏起来 /
    //    禁用掉的路径等于没有路径 —— 那正是这次要修的「功能在，只是看不见」。
    const triggers = [
      ...container.querySelectorAll(
        '[data-qy-test-criteria] [data-slot="select-trigger"]'
      ),
    ] as HTMLElement[]
    assert.equal(
      triggers.length,
      2,
      '试跑区里没有阶段 / 匹配方式这两个可点的选择器'
    )
    for (const trigger of triggers) {
      assert.equal(
        trigger.closest(
          '[hidden],[style*="display:none"],[style*="display: none"]'
        ),
        null,
        '判据选择器藏在一个不可见的祖先里：存在但看不见，与静默省略同形'
      )
      assert.ok(!trigger.hasAttribute('disabled'), '判据选择器点不动')
    }
    // 选择器绑的是这条规则此刻的判据，不是一个空壳：Base UI 把选中值落在
    // 每个 Select 自带的隐藏 input 上，那是「它现在指着哪一档」最直接的证据。
    const bound = [
      ...container.querySelectorAll('[data-qy-test-criteria] input'),
    ].map((node) => node.getAttribute('value'))
    assert.deepEqual(
      bound.sort(),
      ['keyword', 'prompt'],
      '两个判据选择器没有绑到这条规则此刻的阶段与匹配方式'
    )
    // 而且**下拉关着的时候就要写译名**。Base UI 的 `<Select.Value />` 只从
    // Root 的 `items` 里查译名，不传就直接渲染原始取值 —— 一个写着 `prompt` /
    // `keyword` 的判据条，和不摆判据没有区别。
    assert.ok(
      triggers.some((node) =>
        (node.textContent ?? '').includes(zhText('qy_vio_phase_prompt'))
      ),
      '阶段选择器上写的不是译名（多半是没给 Select 传 items，于是渲染了原始取值）'
    )
    assert.ok(
      triggers.some((node) =>
        (node.textContent ?? '').includes(zhText('qy_vio_match_keyword'))
      ),
      '匹配方式选择器上写的不是译名（同上）'
    )
    for (const trigger of triggers) {
      assert.ok(
        !/\b(prompt|keyword)\b/.test(trigger.textContent ?? ''),
        '判据选择器上直接漏出了枚举原始取值'
      )
    }

    // 3. 会读的那一格在，别的输入格子一个都没有。
    assert.ok(
      placeholders(container).includes(
        zhText('qy_vio_test_input_request_text_ph')
      ),
      '请求上下文这一格不见了'
    )
    assert.ok(
      !placeholders(container).includes(
        zhText('qy_vio_test_input_upstream_text_ph')
      ),
      '这条规则读不到上游正文，却给了一个能填的格子'
    )

    // 4. 缺席的维度**看得见**，且逐条写明为什么。项目方看到的那一屏，
    //    缺的正是这一段。
    const block = absentBlock(container)
    const blockText = block.textContent ?? ''
    for (const id of [
      'upstream_text',
      'reject_reason',
      'status_code',
      'error_code',
      'rate_count',
    ] as QyViolationTestInput[]) {
      assert.ok(
        blockText.includes(zhText(`qy_vio_test_input_${id}`)),
        `「读不到的维度」里没有列出 ${id}`
      )
    }
    assert.ok(
      blockText.includes(
        zhText('qy_vio_test_absent_phase_no_upstream', {
          phase: zhText('qy_vio_phase_prompt'),
        })
      ),
      '上游那几样缺席时没有说明「转发前上游还没返回」'
    )
    assert.ok(
      blockText.includes(
        zhText('qy_vio_test_absent_match_not_rate', {
          match: zhText('qy_vio_match_keyword'),
        })
      ),
      '频率计数缺席时没有说明是匹配方式的缘故'
    )

    // 5. 只读。这一块里一个能填的控件都不许有。
    assert.equal(
      block.querySelectorAll('input,textarea,select,button').length,
      0,
      '「读不到的维度」被做成了能填的格子：填了不生效，比不显示更糟'
    )
  })

  test('在面板里换成错误码判据：格子与说明当场跟着变', async () => {
    const container = await mount('upstream_err', 'keyword')
    assert.ok(
      placeholders(container).includes(
        zhText('qy_vio_test_input_upstream_text_ph')
      ),
      '上游关键词规则一开始就该问上游正文'
    )

    assert.ok(switchTo != null)
    await act(async () => switchTo?.({ matchType: 'error_code' }))
    await act(async () => {})

    // 该出现的出现了。
    assert.ok(
      placeholders(container).includes(zhText('qy_vio_test_input_error_code')),
      '换成错误码判据后没有出现错误码这一格'
    )
    // 该消失的消失了 —— 而且是**变成说明**，不是凭空蒸发。
    assert.ok(
      !placeholders(container).includes(
        zhText('qy_vio_test_input_upstream_text_ph')
      ),
      '错误码判据不读上游正文，那一格却还留着'
    )
    const blockText = absentBlock(container).textContent ?? ''
    assert.ok(
      blockText.includes(zhText('qy_vio_test_input_upstream_text')),
      '上游正文既没了格子、也没进说明 —— 又变回静默省略'
    )
    assert.ok(
      blockText.includes(
        zhText('qy_vio_test_absent_match_not_text', {
          match: zhText('qy_vio_match_error_code'),
        })
      ),
      '三段文本缺席的理由没有跟着换成「匹配方式是错误码」'
    )
    // 判据条上的那句话也必须跟着换，否则它会一直念着旧的匹配方式。
    assert.ok(
      (container.textContent ?? '').includes(
        zhText('qy_vio_test_criteria_now', {
          match: zhText('qy_vio_match_error_code'),
          phase: zhText('qy_vio_phase_upstream_err'),
        })
      ),
      '判据条还念着换之前的匹配方式'
    )
  })

  test('AI 审核那一档：问的是模型结论，且说清楚了为什么不问文本', async () => {
    const container = await mount('prompt', 'ai_review')
    const text = container.textContent ?? ''

    assert.ok(
      text.includes(zhText('qy_vio_test_ai_desc')),
      'AI 审核规则没有解释它问的是模型给出的结论而不是上下文'
    )
    for (const key of [
      'qy_vio_test_input_ai_verdict',
      'qy_vio_test_input_ai_category',
      'qy_vio_test_input_ai_confidence',
    ]) {
      assert.ok(text.includes(zhText(key)), `AI 审核少了 ${key} 这一格`)
    }
    const blockText = absentBlock(container).textContent ?? ''
    assert.ok(
      blockText.includes(
        zhText('qy_vio_test_absent_match_not_text', {
          match: zhText('qy_vio_match_ai_review'),
        })
      ),
      'AI 审核规则没有说明它为什么不问任何文本'
    )
    assert.ok(
      blockText.includes(zhText('qy_vio_test_absent_readonly')),
      '缺席维度那一块没有写明它们是只读说明'
    )
  })
})

/*
 * 变异实验（逐条改产品代码跑一次，跑完还原；基线 96 pass / 0 fail）：
 *
 *  M1 `qyRuleTestAbsentDimensions` 直接 `return []`（退回静默省略）  → 11 红
 *  M2 `status_code` 的缺席理由写死 `phase_no_upstream`               →  2 红
 *  M3 `textual` 里漏掉 `upstream_text`（文本判据被当成非文本判据）   →  2 红
 *  M4 缺席行改成渲染 `<Input readOnly>`（只读说明变成能填的格子）    →  4 红
 *  M5a 判据条整块加 `hidden`（路径在 DOM 里但看不见）                →  3 红
 *  M5b 两个判据选择器整块删掉（改判据没有就近路径）                  →  4 红
 *  M6 `criteria` 插值把 phase 与 match 对调（说明念错了另一个下拉）  →  3 红
 *  M7 `rule-form-sheet` 的 `onMatchTypeChange` 改成空函数（选了不生效）→ 1 红
 *  M8 两个 Select 去掉 `items`（下拉上漏出 `prompt` / `keyword`）    →  3 红
 *
 * M3 与 M5a 是**第一轮活下来的两个**：M3 因为表里当时没有一条 `upstream_text`
 * 匹配方式的用例，M5a 因为只数了选择器的个数、没管它可不可见。两条断言都是
 * 补上之后才有的 —— 记在这里，免得下一次又被同样的形状绕过去。
 */
