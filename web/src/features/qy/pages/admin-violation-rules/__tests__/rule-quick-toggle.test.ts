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

import { qyToggleNeedsConfirm } from '../lib/rule-toggle'
import type { QyViolationRule } from '../types'

/**
 * rule-quick-toggle.test.ts —— 规则列表行内快速启停的回归。
 *
 * 项目方原话：「规则集这里加一个快速启用关闭的按钮」。这个开关有三条不能塌的边：
 *
 *  1. **它不能走整体更新接口。** 那个接口提交的是前端手上那一整份规则，而它是
 *     列表页在 15 秒 staleTime 里拉下来的拷贝 —— 用它翻开关，等于把这期间别人对
 *     pattern / mode / 作用域的改动一起写回旧值。这是本仓库 applyUpgrade 注释里
 *     写过的同一个坑，那里的结论也是「只写需要的列」。
 *  2. **失败必须回滚到调用前那一份缓存。** 乐观更新之后接口失败却不回滚，界面就
 *     长期显示一个与线上相反的状态 —— 而这一页的状态是「这条防护规则现在在不在跑」。
 *  3. **二次确认只拦真正有代价的方向。** 全拦就不叫「快速」，全不拦就把「关掉一条
 *     没有任何症状的防护」交给手滑。
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
    status_scope: '',
    model_scope: '',
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

describe('二次确认的取舍', () => {
  test('任何停用都要确认 —— 停用一条防护规则没有任何症状', () => {
    // 接口照常 200、业务照常跑，只是从此零命中。这与「内置规则包从没导入过」
    // 完全同形，而那一次是靠项目方问「我刷新没有看到我需要的功能」才被发现的。
    assert.equal(qyToggleNeedsConfirm(rule({ mode: 'shadow' }), false), true)
    assert.equal(qyToggleNeedsConfirm(rule({ mode: 'enforce' }), false), true)
  })

  test('启用真实模式规则要确认 —— 它下一秒开始真的扣费封号', () => {
    assert.equal(qyToggleNeedsConfirm(rule({ mode: 'enforce' }), true), true)
  })

  test('启用影子规则一点即生效 —— 那正是「快速」要服务的场景', () => {
    // 影子只记录，不扣钱、不阻断、不计数。导入内置规则包之后逐条打开是这一页
    // 最高频的动作，在它上面加一次弹窗就等于把「快速启用关闭」这个需求做没了。
    assert.equal(qyToggleNeedsConfirm(rule({ mode: 'shadow' }), true), false)
    // 未知 / 空 mode 与后端判据同向（`mode === 'enforce'` 才算真实），
    // 一律按影子处理 —— 反过来会在一个安全的动作上弹一个吓人的框。
    assert.equal(qyToggleNeedsConfirm(rule({ mode: '' as never }), true), false)
  })
})

describe('启停走的是只写一列的独立接口', () => {
  const api = read('api.ts')

  test('用 PATCH 打到 /rules/:id/enabled，而不是整体更新', () => {
    assert.match(
      api,
      /qyPatch<[^>]*>\(\s*`\/admin\/violation\/rules\/\$\{id\}\/enabled`/,
      '启停没有走只写 enabled 一列的独立路由'
    )
    // 反向：整体更新接口必须还在（编辑抽屉在用），但启停函数不许碰它。
    const toggleFn = api.slice(
      api.indexOf('export function setQyViolationRuleEnabled'),
      api.indexOf('/** 软删')
    )
    assert.ok(toggleFn.length > 0, '找不到 setQyViolationRuleEnabled')
    assert.ok(
      !toggleFn.includes('qyPut'),
      '启停函数在用 PUT 整体更新 —— 那会把前端手上那份过期拷贝整个写回库'
    )
  })

  test('页面用的是启停接口，不是 updateQyViolationRule', () => {
    const page = read('index.tsx')
    assert.ok(
      page.includes('setQyViolationRuleEnabled'),
      '页面没有接上启停接口'
    )
    assert.ok(
      !page.includes('updateQyViolationRule'),
      '列表页直接引用了整体更新接口 —— 启停一旦改用它就是一次静默回滚'
    )
  })
})

describe('列表行内的开关真的被渲染了，且失败会回滚', () => {
  const page = read('index.tsx')

  test('状态列渲染了 Switch，并挂上了启停请求', () => {
    // 「函数写好了却从没被渲染」是本仓库排第一的断链形状：行为测试全绿，
    // 页面上没有入口。必须匹配 JSX 开标签，裸组件名会被 import 那一行满足。
    assert.match(page, /<Switch[\s/>]/, '规则行里没有开关')
    assert.ok(
      page.includes('onCheckedChange'),
      '开关没有接任何回调 —— 点了不会发生任何事'
    )
    assert.ok(
      page.includes('requestToggle(row, checked === true)'),
      '开关没有把这一行和目标状态交给启停流程'
    )
    assert.ok(
      page.includes('qyToggleNeedsConfirm'),
      '二次确认的取舍没有被页面消费 —— 那等于全都不拦'
    )
  })

  test('乐观更新 + 失败回滚 + toast 三件齐全', () => {
    assert.ok(
      page.includes('onMutate') && page.includes('cancelQueries'),
      '没有乐观更新；不取消在途查询的话，一次 15 秒前发出的列表请求落地会把开关按回旧值'
    )
    assert.ok(
      page.includes('context.previous'),
      '失败没有回滚到调用前那一份缓存 —— 界面会长期显示一个与线上相反的状态'
    )
    assert.ok(
      page.includes('toast.error(qyOpsErrorMessage(error, t))'),
      '启停失败没有任何提示'
    )
    // changed=false 是「什么都没发生」（重复点击 / 别人抢先改成同一个值），
    // 后端在这条路径上不写审计。报成「已启用」会让人以为自己改动过状态。
    assert.ok(
      page.includes('result.changed'),
      '没有区分「真的改了」与「什么都没发生」'
    )
    // 统计里的「影子 N 条 / 真实 N 条」只数启用中的规则。
    assert.ok(
      page.includes('qyKeys.adminViolationStats()'),
      '启停之后没有失效统计 —— 顶部横幅会继续显示一个已经不成立的口径'
    )
    // 内置规则包面板的「已停用」徽标读的正是 rule_enabled，而那份查询有 15 秒
    // staleTime：不一起失效的话，刚停用的规则在面板上仍是「已是最新 + 影子」，
    // 管理员据此点「导入 / 同时升级」，以为规则会跑起来 —— 而导入一个字节都不
    // 动 enabled。那个徽标存在的全部理由就是堵这个洞，它自己不能有这 15 秒。
    assert.ok(
      page.includes('qyKeys.adminViolationBuiltin()'),
      '启停之后没有失效内置规则包面板 —— 那个「已停用」徽标会在 15 秒内说反话'
    )
  })
})

describe('被停用的内置规则在导入面板上必须看得见', () => {
  /**
   * 后端在已存在的内置规则行上只 UPDATE pattern / case_sensitive 与四列元数据，
   * enabled 一个字节都不动（见 importOne 与后端的
   * TestDisabledBuiltinRuleStaysDisabledAcrossReimport）。所以停用过的内置规则
   * **不会**被下一次导入重新打开 —— 这是对的，但它必须在界面上说出来：
   * 否则那一行只显示「已是最新 + 影子」，看上去一切正常，而它一条都抓不到。
   */
  const sheet = read('components/builtin-pack-sheet.tsx')

  test('停用状态被单独标出来，并解释了导入不会把它打开', () => {
    assert.ok(
      sheet.includes('!item.rule_enabled'),
      '导入面板没有读 rule_enabled —— 停用过的内置规则会伪装成「一切正常」'
    )
    assert.ok(
      sheet.includes('qy_vio_builtin_rule_disabled'),
      '缺少「已停用」标记'
    )
    assert.ok(
      sheet.includes('qy_vio_builtin_disabled_hint'),
      '缺少「重新导入不会把它打开」的说明 —— 那是运营点完导入之后唯一能解释' +
        '「为什么它还是关着」的东西'
    )
  })
})
