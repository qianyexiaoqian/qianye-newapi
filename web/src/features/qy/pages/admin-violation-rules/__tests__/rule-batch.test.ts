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
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { describe, test } from 'node:test'

import {
  QY_VIOLATION_BATCH_SCOPE_OPS,
  qyBatchEnableChangeCount,
  qyBatchNoteworthyItems,
  qyBatchResultTone,
  qyEnforceRulesPendingEnable,
} from '../lib/rule-batch'
import type { QyViolationBatchResult, QyViolationRule } from '../types'

/**
 * rule-batch.test.ts —— 规则列表多选批量操作的回归。
 *
 * 项目方原话：「违规规则配置，增加一个多选，可以批量进行作用分组的划分，启动，禁用。」
 *
 * 这一层守四条边：
 *
 *  1. **「覆盖还是追加」不许让人猜。** 这是本仓反复出问题的形状 —— 一次以为在追加、
 *     实际在覆盖的批量，会把一批规则原有的作用分组整串抹掉，而列表上那几条规则
 *     看起来一个字都没改。所以三种写法各自是一个必选项 + 一句说清后果的说明。
 *  2. **影响面必须是真数字。** 「已勾选 20 条」不等于「20 条会变」。两个数字对不上时
 *     人就不再相信这个确认框，而下一次真正危险的那个框也会被同样地划过去。
 *  3. **批量不是 mode 的第二个入口。** 影子 / 真实是本模块唯一决定「要不要真的
 *     扣钱封号」的开关，批量入口看不到 pattern 与作用域这些做判断必需的上下文。
 *  4. **界面上真的有多选，且真的接上了批量。** 「函数写好了却从没被渲染」是本仓库
 *     排第一的断链形状：行为测试全绿，页面上没有入口。
 */

const SRC = new URL('..', import.meta.url)

function read(relative: string): string {
  return readFileSync(new URL(relative, SRC), 'utf-8')
}

function rule(patch: Partial<QyViolationRule>): QyViolationRule {
  return {
    id: 1,
    name: 'r',
    remark: '',
    public_reason: '',
    enabled: true,
    mode: 'shadow',
    source: 'manual',
    builtin_key: '',
    builtin_version: 0,
    builtin_fingerprint: '',
    priority: 100,
    phase: 'prompt',
    match_type: 'keyword',
    pattern: 'x',
    case_sensitive: false,
    model_scope: '',
    status_scope: '',
    group_scope: '',
    group_scope_mode: 'include',
    action: 'record',
    fee_mode: 'none',
    fee_fixed: '0',
    fee_multiple: '0',
    fee_max_quota: 0,
    count_weight: 1,
    severity: 1,
    archive_context: false,
    block_message: '',
    created_at: 0,
    updated_at: 0,
    created_by: 0,
    updated_by: 0,
    ...patch,
  }
}

function result(
  patch: Partial<QyViolationBatchResult>
): QyViolationBatchResult {
  return { total: 0, succeeded: 0, skipped: 0, failed: 0, items: [], ...patch }
}

describe('批量启用的影响面是真数字', () => {
  test('只算「当前停用的真实模式规则」—— 已启用的再点一次是空操作', () => {
    // 把已经启用的 enforce 规则也算进警告数字，会让数字虚高；而虚高的警告
    // 会训练人闭着眼睛点确认，下一次真的危险时同样被划过去。
    const rules = [
      rule({ id: 1, mode: 'enforce', enabled: false }),
      rule({ id: 2, mode: 'enforce', enabled: true }),
      rule({ id: 3, mode: 'shadow', enabled: false }),
    ]
    assert.deepEqual(
      qyEnforceRulesPendingEnable(rules).map((item) => item.id),
      [1]
    )
  })

  test('未知 / 空 mode 按影子算，与后端同向', () => {
    // 后端的判据是 `mode === 'enforce'`（未知取值一律按影子兜底）。前端反过来
    // 会在一个安全的动作上弹一个吓人的框 —— 同样是在训练人无视警告。
    assert.equal(
      qyEnforceRulesPendingEnable([rule({ mode: '' as never, enabled: false })])
        .length,
      0
    )
  })

  test('「会真的改变」与「已勾选」是两个数字', () => {
    const rules = [
      rule({ id: 1, enabled: true }),
      rule({ id: 2, enabled: false }),
      rule({ id: 3, enabled: false }),
    ]
    assert.equal(qyBatchEnableChangeCount(rules, true), 2)
    assert.equal(qyBatchEnableChangeCount(rules, false), 1)
  })
})

describe('批次结果的颜色由响应体决定，不是 HTTP 状态码', () => {
  test('有失败就是红的 —— 后端整批一律 200', () => {
    // 逐条明细才是这个接口的产品，`success:false` 会让 qy 的 unwrap 把 data
    // 整个丢掉。所以「接口成功」不等于「事情做成了」。
    assert.equal(
      qyBatchResultTone(result({ total: 3, succeeded: 2, failed: 1 })),
      'error'
    )
    assert.equal(
      qyBatchResultTone(result({ total: 3, succeeded: 0, failed: 3 })),
      'error'
    )
  })

  test('一条都没改动是黄的，不是绿的 —— 那通常意味着选错了范围', () => {
    assert.equal(
      qyBatchResultTone(result({ total: 3, succeeded: 0, skipped: 3 })),
      'warning'
    )
  })

  test('真的改动了且零失败才是绿的', () => {
    assert.equal(
      qyBatchResultTone(result({ total: 3, succeeded: 2, skipped: 1 })),
      'success'
    )
  })

  test('报告只摊开失败与跳过 —— 成功项不占屏幕', () => {
    const res = result({
      total: 3,
      items: [
        { id: 1, name: 'a', outcome: 'ok' },
        { id: 2, name: 'b', outcome: 'skipped', code: 'x' },
        { id: 3, name: 'c', outcome: 'failed', code: 'y' },
      ],
    })
    assert.deepEqual(
      qyBatchNoteworthyItems(res).map((item) => item.id),
      [2, 3]
    )
  })
})

describe('「覆盖 vs 追加」在界面上被说清楚了', () => {
  const bar = read('components/rule-batch-bar.tsx')

  test('三种写法都在，且各自有一句独立的后果说明', () => {
    assert.deepEqual([...QY_VIOLATION_BATCH_SCOPE_OPS].sort(), [
      'append',
      'remove',
      'replace',
    ])
    // 必须是**每种写法各一句说明**，而不是一句笼统的「批量设置分组」。
    assert.ok(
      bar.includes('qy_vio_batch_op_${item}_desc'),
      '三种写法没有各自的后果说明 —— 那就等于让人猜覆盖还是追加'
    )
    // 必须是单选而不是一个只有两态的开关：三种写法是并列的三件事。
    assert.match(bar, /<RadioGroup[\s>]/, '写法不是一个必选的单选')
  })

  test('每种写法的确认文案是独立的一句话', () => {
    // 三种写法的后果完全不同（保留原有 / 丢弃原有 / 摘掉几个），
    // 共用一句「确定要修改 N 条规则吗」等于什么都没说。
    assert.ok(
      bar.includes('qy_vio_batch_scope_confirm_${op}'),
      '三种写法共用了同一句确认文案'
    )
    const zh = JSON.parse(
      readFileSync(new URL('../../../../i18n/qy/zh.json', SRC), 'utf-8')
    ) as Record<string, string>
    for (const op of QY_VIOLATION_BATCH_SCOPE_OPS) {
      const text = zh[`qy_vio_batch_scope_confirm_${op}`]
      assert.ok(text != null, `缺少 ${op} 的确认文案`)
      assert.match(
        text,
        /\{\{count\}\}/,
        `${op} 的确认文案没有说会影响多少条规则`
      )
    }
    // 覆盖会丢掉每条规则原有的分组名单，必须走不可逆确认（强制勾选）。
    assert.ok(
      bar.includes("irreversible={op === 'replace'}"),
      '「覆盖」没有走不可逆确认 —— 被覆盖掉的旧作用域从界面上再也拿不回来'
    )
  })

  test('方向是必选项，且解释了为什么必须选', () => {
    // 同一串分组名在 include / exclude 下含义完全相反：给一条豁免名单追加
    // vip，是多豁免了一个分组，而操作者以为自己多防了一个。
    assert.ok(
      bar.includes('group_scope_mode: scopeMode'),
      '批量请求没有带上名单方向 —— 后端会 400，而这个字段本来就不该有默认值'
    )
    assert.ok(
      bar.includes('qy_vio_batch_scope_mode_desc'),
      '没有解释两个方向的差别'
    )
    assert.ok(
      bar.includes('qy_vio_batch_scope_mode_mismatch_hint'),
      '没有说明「追加/移除不会替规则翻转方向」—— 那些规则会被静默跳过，' +
        '而人以为自己改了全部'
    )
  })

  test('「覆盖 + 空名单」= 清空作用域，必须单独警告', () => {
    // 这是一次放宽：这批规则从此对全站所有模型分组生效。它是合法的，
    // 但绝不能靠「填空框然后点确定」静默发生。
    assert.ok(
      bar.includes('qy_vio_batch_scope_clear_title'),
      '清空作用域没有任何警示'
    )
    assert.ok(
      bar.includes("disabled={op !== 'replace' && groups.length === 0}"),
      '追加/移除允许提交一个空名单 —— 那是一次什么都不做的请求'
    )
  })
})

describe('批量不是 mode 的第二个入口', () => {
  const bar = read('components/rule-batch-bar.tsx')
  const api = read('api.ts')

  test('批量请求体里没有规则的 mode 字段', () => {
    // 把一批规则从影子切成真实，下一秒就开始真的扣费、阻断、累计封号，
    // 而批量入口看不到 pattern 与作用域这些做判断必需的上下文。
    //
    // `group_scope_mode`（名单方向）是另一回事，先把它换成一个不含 mode 的名字，
    // 免得这条断言被它满足而永远绿着。
    const batchFns = api
      .slice(
        api.indexOf('export function batchSetQyViolationRulesEnabled'),
        api.indexOf('/** 软删')
      )
      .replaceAll('group_scope_mode', 'group_scope_direction')
    assert.ok(batchFns.length > 0, '找不到批量接口')
    assert.ok(
      !/mode\s*[:?]/.test(batchFns),
      '批量接口的请求体里出现了 mode —— 那是「真拦人」的开关'
    )
    const payloads = bar
      .slice(
        bar.indexOf('const enableMutation'),
        bar.indexOf('const changeCount')
      )
      .replaceAll('group_scope_mode', 'group_scope_direction')
    assert.ok(!/mode\s*:/.test(payloads), '批量操作条往请求里塞了 mode')
  })

  test('启用一批真实模式规则要单独确认，并列出是哪几条', () => {
    assert.ok(
      bar.includes('qyEnforceRulesPendingEnable'),
      '批量启用没有算过「其中几条会被送进真实执行」'
    )
    assert.ok(
      bar.includes('qy_vio_batch_enforce_title'),
      '缺少「其中 N 条是真实模式」的影响面提示'
    )
    assert.ok(
      bar.includes(
        'irreversible={pendingEnable === true && pendingEnforce.length > 0}'
      ),
      '只有「批量启用里确实有真实模式规则」才该升级成强制勾选;' +
        '全都强制勾选只会训练人闭着眼睛勾，一律不勾则等于没有闸'
    )
  })
})

describe('批量走的是独立路由，且真的接在页面上', () => {
  const api = read('api.ts')
  const page = read('index.tsx')

  test('两个动作各一条路由，不是一个万能批量接口', () => {
    assert.match(
      api,
      /qyPost<QyViolationBatchResult>\(\s*'\/admin\/violation\/rules\/batch\/enabled'/,
      '批量启停没有走独立路由'
    )
    assert.match(
      api,
      /qyPost<QyViolationBatchResult>\(\s*'\/admin\/violation\/rules\/batch\/group-scope'/,
      '批量作用分组没有走独立路由'
    )
  })

  test('列表页真的渲染了勾选框与批量条', () => {
    // 必须匹配 JSX 开标签，裸组件名会被 import 那一行满足。
    assert.match(page, /<Checkbox[\s/>]/, '规则列表里没有勾选框')
    assert.match(page, /<QyRuleBatchBar[\s/>]/, '批量操作条没有被渲染')
    assert.ok(
      page.includes('qy_vio_batch_select_all'),
      '表头缺少全选，而全选正是「多选」这个需求最先被点的地方'
    )
    assert.ok(
      page.includes('selectedRules.length > 0 &&'),
      '批量条常驻显示 —— 一行永远无事可做的空工具栏'
    )
  })

  test('换页 / 换筛选清空勾选', () => {
    // 不清的话勾选集里会留着一批屏幕上已经看不见的规则，而批量按钮上的数字
    // 照旧把它们算进去 —— 那是一次没有人看过内容的批量。
    assert.match(
      page,
      /useEffect\(\(\) => \{\s*setSelectedIds\(new Set\(\)\)\s*\}, \[params\]\)/,
      '换页 / 换筛选之后勾选没有被清空'
    )
    // 影响面读的是当前页的规则行，而不是一份跨页攒下来的过期拷贝。
    assert.ok(
      page.includes('rules.filter((rule) => selectedIds.has(rule.id))'),
      '勾选集没有与当前页取交集'
    )
  })
})
