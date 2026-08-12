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

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import {
  QY_VIOLATION_MODES,
  qyEmptyViolationRule,
  qyViolationRuleSchema,
  qyViolationRuleToForm,
  qyViolationRuleToPayload,
  type QyViolationRuleFormValues,
} from '../lib/rule-form'
import type { QyViolationRule } from '../types'

/**
 * rule-mode.test.ts —— 模式简化(需求 A)的前端回归。
 *
 * 项目方原话:「影子模式、真实模式是绑定到规则上,当前你这个切来切去的本来简单的
 * 功能搞得那么复杂。」删掉全局层之后,前端必须同时满足三件事,任何一件漏掉都会让
 * 那个抱怨原样回来:
 *
 *  1. 规则表单上有且只有一个模式字段,默认落在**不扣钱**的那一侧;
 *  2. 未知/空 mode 一律显示成影子(与后端 `mode === 'enforce'` 的判据同向);
 *  3. 全局模式的接口、按钮、文案**全部**消失 —— 只删一半就是留一个改不动任何
 *     东西的控件,那比不删更糟。
 */

const SRC = new URL('..', import.meta.url)

function read(relative: string): string {
  return readFileSync(new URL(relative, SRC), 'utf-8')
}

function rule(patch: Partial<QyViolationRuleFormValues>) {
  return { ...qyEmptyViolationRule(), name: 'r', ...patch }
}

function issuePaths(values: QyViolationRuleFormValues): string[] {
  const parsed = qyViolationRuleSchema.safeParse(values)
  if (parsed.success) return []
  return parsed.error.issues.map((issue) => issue.path.join('.'))
}

/** 造一条后端形状的规则行,只覆盖被测字段。 */
function serverRule(patch: Partial<QyViolationRule>): QyViolationRule {
  return {
    id: 1,
    name: 'r',
    remark: '',
    category_id: 0,
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
    archive_context: false,
    block_message: '',
    created_at: 0,
    updated_at: 0,
    created_by: 0,
    updated_by: 0,
    ...patch,
  }
}

describe('规则执行模式', () => {
  test('只有影子 / 真实两档,影子在前', () => {
    // 顺序即界面顺序。影子在前是因为它是安全的那一侧,而这一页最重的一个动作
    // 就是把某条规则从影子切成真实。
    assert.deepEqual(QY_VIOLATION_MODES, ['shadow', 'enforce'])
  })

  test('新规则默认影子', () => {
    // 一条 `.*` 正则能在 30 秒内封掉全站用户。默认真实执行就是把这件事交给手滑。
    assert.equal(qyEmptyViolationRule().mode, 'shadow')
  })

  test('schema 只接受这两个取值', () => {
    assert.deepEqual(issuePaths(rule({ mode: 'shadow' })), [])
    assert.deepEqual(issuePaths(rule({ mode: 'enforce' })), [])
    for (const bad of ['', 'dry_run', 'ENFORCE', 'true']) {
      assert.ok(
        issuePaths(rule({ mode: bad as never })).includes('mode'),
        `mode=${JSON.stringify(bad)} 必须被拒`
      )
    }
  })

  test('后端读回来的未知 mode 一律显示成影子', () => {
    // 后端判据是 `mode === 'enforce'`。前端把未知值读成真实,界面就会显示一个
    // 与线上行为**相反**的状态 —— 那是最贵的一种误判:管理员以为在扣钱,其实没扣;
    // 或者以为没扣,其实在扣。
    assert.equal(
      qyViolationRuleToForm(serverRule({ mode: 'enforce' })).mode,
      'enforce'
    )
    assert.equal(
      qyViolationRuleToForm(serverRule({ mode: 'shadow' })).mode,
      'shadow'
    )
    for (const unknown of ['', 'ENFORCE', 'dry_run']) {
      assert.equal(
        qyViolationRuleToForm(serverRule({ mode: unknown as never })).mode,
        'shadow',
        `mode=${JSON.stringify(unknown)} 必须读成影子`
      )
    }
  })

  test('提交时原样透传 mode', () => {
    // 断链回归:表单里选了 enforce、payload 里没带上,表现是「保存成功但一直是影子」。
    assert.equal(
      qyViolationRuleToPayload(rule({ mode: 'enforce' })).mode,
      'enforce'
    )
    assert.equal(
      qyViolationRuleToPayload(rule({ mode: 'shadow' })).mode,
      'shadow'
    )
  })
})

describe('全局模式层已彻底删除', () => {
  /**
   * 只删一半是本仓库反复出现的形状:接口没了、按钮还在,或者反过来。
   * 这里直接对整个功能目录做字面量断言 —— 任何一处残留都会红。
   */
  const files = [
    'api.ts',
    'types.ts',
    'index.tsx',
    'lib/rule-form.ts',
    'components/rule-form-sheet.tsx',
    'components/violation-shadow-banner.tsx',
  ]

  test('dry_run 与全局模式接口在这一页一个字都不剩', () => {
    for (const file of files) {
      const source = read(file)
      assert.ok(!source.includes('dry_run'), `${file} 仍在引用 dry_run`)
      assert.ok(
        !source.includes('setQyViolationShadowMode'),
        `${file} 仍在调用已删除的全局模式接口`
      )
      assert.ok(
        !source.includes("'/admin/violation/mode'"),
        `${file} 仍在引用已删除的路由 /admin/violation/mode`
      )
    }
  })

  test('横幅不再渲染任何模式切换按钮', () => {
    const banner = read('components/violation-shadow-banner.tsx')
    for (const key of [
      'qy_vio_mode_set_shadow',
      'qy_vio_mode_set_live',
      'qy_vio_mode_follow_yaml',
      'shadow_override',
      'global_shadow',
      'config_shadow',
    ]) {
      assert.ok(!banner.includes(key), `横幅仍在引用已删除的 ${key}`)
    }
    // 反向:熔断那条路必须还在。删掉全局开关顺手把熔断也删掉,等于删了一道安全网。
    assert.ok(banner.includes('forced_shadow'), '熔断告警不见了')
    assert.ok(banner.includes('qy_vio_breaker_reset'), '解除熔断的按钮不见了')
  })

  test('模式字段真的被表单渲染了', () => {
    // 「字段定义了但没挂进表单」= 保存的永远是默认值。行为断言(schema/payload)
    // 证明不了这一点,只有直接钉住引用可以。
    const sheet = read('components/rule-form-sheet.tsx')
    assert.ok(sheet.includes("name='mode'"), '规则表单没有渲染 mode 字段')
    assert.ok(
      sheet.includes('QY_VIOLATION_MODES'),
      '模式下拉没有用统一的取值表'
    )
    assert.ok(
      sheet.includes('qy_vio_field_mode_enforce_desc'),
      '切真实模式的警示文案没有被渲染'
    )
  })
})

describe('内置规则包与影子命中面板真的被挂上了', () => {
  /**
   * 「组件写好了却从没被渲染」是本仓库排第一的断链形状。行为测试覆盖不到它 ——
   * 组件自己的单测会全绿,而页面上根本没有入口。
   */
  const page = read('index.tsx')

  test('页面渲染了这两个组件,并且给了入口', () => {
    // 必须匹配 JSX **开标签**而不是裸组件名:后者会被 import 那一行满足,
    // 于是「组件导入了但从没渲染」这个形状可以原封不动地溜过去 ——
    // 实测过的一次假回归(把 <QyBuiltinPackSheet 改成 <QyBuiltinPackSheetX 后
    // 断言照样全绿)。
    assert.match(
      page,
      /<QyBuiltinPackSheet[\s/>]/,
      '内置规则包组件没有被页面渲染'
    )
    assert.match(page, /<QyShadowHitsSheet[\s/>]/, '影子命中面板没有被页面渲染')
    assert.ok(
      page.includes('setBuiltinOpen(true)'),
      '内置规则包没有任何可以打开它的入口'
    )
    assert.ok(
      page.includes('setShadowRule(row)'),
      '影子命中面板没有挂在规则行上 —— 从「改规则」到「看它抓到了什么」必须是一次点击'
    )
  })

  test('影子命中面板真的按 shadow=1 + rule_id 筛,并且能导出', () => {
    const sheet = read('components/shadow-hits-sheet.tsx')
    // 列表查询与导出查询是两个独立的参数对象,必须各自断言。只写一条
    // `includes("shadow: '1'")` 的话,把列表那一处改坏、导出那一处留着,
    // 断言照样全绿 —— 实测过的一次假回归。
    assert.match(
      sheet,
      /const params = \{[^}]*shadow: '1'/s,
      '列表查询没有按影子筛,面板会混进真实命中'
    )
    assert.match(
      sheet,
      /exportQyViolationRecords\(\{[^}]*shadow: '1'/s,
      '导出没有按影子筛,导出来的文件与屏幕上看到的不是同一批行'
    )
    assert.ok(sheet.includes('rule_id: ruleId'), '没有按规则筛')
    assert.ok(
      sheet.includes('exportQyViolationRecords'),
      '导出没接上 —— 「抓日志做分析」这个用例到这里就断了'
    )
    assert.ok(
      sheet.includes('fee_quota_want'),
      '少了「若真实执行会扣多少」,这份面板做不了成本评估'
    )
  })
})

describe('i18n 文案', () => {
  const required = [
    ...QY_VIOLATION_MODES.map((v) => `qy_vio_mode_${v}`),
    'qy_vio_field_mode',
    'qy_vio_field_mode_shadow_desc',
    'qy_vio_field_mode_enforce_desc',
    'qy_vio_mode_all_shadow_title',
    'qy_vio_mode_all_shadow_desc',
    'qy_vio_mode_mixed_title',
    'qy_vio_mode_mixed_desc',
    'qy_vio_mode_shadow_hits',
    'qy_vio_breaker_title',
    'qy_vio_breaker_desc',
    'qy_vio_breaker_reason',
    'qy_vio_breaker_until',
    'qy_vio_breaker_reset',
    'qy_vio_source_builtin',
    'qy_vio_builtin_open',
    'qy_vio_builtin_title',
    'qy_vio_builtin_desc',
    'qy_vio_builtin_shadow_title',
    'qy_vio_builtin_shadow_desc',
    'qy_vio_builtin_upgrade_label',
    'qy_vio_builtin_import_all',
    'qy_vio_builtin_import_selected',
    'qy_vio_builtin_import_done',
    'qy_vio_builtin_false_positive',
    'qy_vio_builtin_advice',
    'qy_vio_builtin_origin',
    'qy_vio_builtin_modified_hint',
    'qy_vio_builtin_state_not_imported',
    'qy_vio_builtin_state_up_to_date',
    'qy_vio_builtin_state_upgradable',
    'qy_vio_builtin_state_modified',
    'qy_vio_shadow_hits_open',
    'qy_vio_shadow_hits_title',
    'qy_vio_shadow_hits_desc',
    'qy_vio_shadow_hits_total',
    'qy_vio_shadow_hits_export',
    'qy_vio_field_fee_quota_want',
    'qy_vio_field_shadow_reason',
    'qy_vio_shadow_reason_rule_mode',
    'qy_vio_shadow_reason_breaker',
    'qy_vio_shadow_reason_dup_builtin_fee',
  ]

  test('en / zh 都齐全', () => {
    for (const key of required) {
      assert.ok(key in en, `en.json 缺少 ${key}`)
      assert.ok(key in zh, `zh.json 缺少 ${key}`)
    }
  })

  test('en / zh 键数相等', () => {
    // 两边键数不等意味着某一侧漏了 —— 而漏掉的那一侧在界面上直接显示 key。
    assert.equal(Object.keys(en).length, Object.keys(zh).length)
  })

  test('已删除的全局模式文案不再留在词表里', () => {
    // 零消费方的文案会被下一个人当成"还有这个功能"而重新接回去。
    for (const key of [
      'qy_vio_field_dry_run',
      'qy_vio_mode_set_shadow',
      'qy_vio_mode_set_live',
      'qy_vio_mode_follow_yaml',
      'qy_vio_mode_source_yaml',
      'qy_vio_mode_source_settings',
      'qy_vio_mode_saved',
    ]) {
      assert.ok(!(key in en), `en.json 仍留着已删除的 ${key}`)
      assert.ok(!(key in zh), `zh.json 仍留着已删除的 ${key}`)
    }
  })

  test('切真实模式的文案必须明确说出后果', () => {
    // 这一句是管理员按下那个开关之前唯一会读到的东西。它含糊,就等于没有警告。
    const strings = zh as Record<string, string>
    assert.match(strings.qy_vio_field_mode_enforce_desc, /扣费/)
    assert.match(strings.qy_vio_field_mode_enforce_desc, /封号/)
    assert.match(strings.qy_vio_builtin_shadow_desc, /只记录/)
  })
})
