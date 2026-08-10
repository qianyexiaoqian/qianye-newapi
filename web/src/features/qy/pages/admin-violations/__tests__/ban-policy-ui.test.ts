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

/**
 * ban-policy-ui.test.ts —— 处置策略档界面的结构性守卫。
 *
 * 这几条断言看起来"只是在读源码"，但它们守的每一条都是**改一行就静默失效、
 * 而且没有任何其他测试会红**的东西：
 *
 *  1. 兜底档不渲染删除按钮。渲染一个点了必然报错的按钮，会让人以为是系统坏了，
 *     而不是这件事本来就不该做 —— 而后端那道 400 只在被点之后才说话。
 *  2. 保存分两步（先看影响面、再点确认）。把 `confirm: true` 直接挂在保存按钮上
 *     是最容易的"简化"，而那一步简化掉的正是这个功能存在的理由。
 *  3. 影响面永远渲染，包括 0 与算不出来两种。一个时有时无的数字，会让人以为
 *     没显示就等于没有影响。
 */

const PAGE = new URL('..', import.meta.url)

function read(relative: string): string {
  return readFileSync(new URL(relative, PAGE), 'utf-8')
}

describe('处置策略档界面', () => {
  const dialog = read('components/ban-policy-dialog.tsx')
  const tab = read('components/violation-ban-policies-tab.tsx')
  const page = read('index.tsx')

  test('策略档与封禁列表同页', () => {
    assert.ok(
      page.includes('QyViolationBanPoliciesTab'),
      '阈值决定谁被封，它必须和封禁列表在同一页 —— 否则调完阈值看不到这条线上有谁'
    )
    assert.ok(page.includes('qy_vio_tab_ban_policies'))
  })

  test('兜底档不渲染删除按钮', () => {
    assert.ok(
      tab.includes('{!policy.is_default && ('),
      '兜底档必须不渲染删除按钮。删掉它等于让没有专属策略的分组落进一个不存在的策略，' +
        '而后端那道 400 只在被点之后才说话。'
    )
  })

  test('保存分两步：先看影响面，再点确认', () => {
    assert.ok(
      dialog.includes('setConfirming(true)'),
      '第一次点击只能进入确认态，不能直接提交'
    )
    assert.ok(
      dialog.includes('qy_vio_policy_confirm_apply'),
      '第二步必须是一个独立的确认按钮'
    )
    assert.ok(
      dialog.includes('qy_vio_policy_confirm_warning'),
      '确认态必须把「会处置几个人」再说一遍'
    )
  })

  test('影响面永远渲染，含 0 与查询失败两种', () => {
    assert.ok(dialog.includes('qy_vio_policy_impact_threshold_off'))
    assert.ok(
      dialog.includes('qy_vio_policy_impact_failed'),
      '算不出影响面是一个必须被看见的状态 —— 静默显示 0 会让一次收紧看起来无害'
    )
    assert.ok(dialog.includes('qy_vio_policy_impact_count'))
  })

  test('新建默认落「仅记录」', () => {
    assert.ok(
      dialog.includes("action: 'record'"),
      '新增一档策略最不该产生的效果就是当场封掉一批人：默认必须是只观察的那一档'
    )
  })

  test('兜底档没有停用开关，也不收分组名', () => {
    assert.ok(
      dialog.includes('{!isDefault && ('),
      '兜底档没有可回落的下一级，停用它等于让没配分组的用户落进一个不存在的策略'
    )
  })

  test('兜底跑在 YAML 上时必须显示告警', () => {
    assert.ok(
      tab.includes('qy_vio_policy_fallback_from_yaml'),
      '此时在这张表里改任何东西都不影响没配分组的用户，不摆出来只有读源码才发现'
    )
  })

  test('中英文案齐备', () => {
    const keys = [
      'qy_vio_tab_ban_policies',
      'qy_vio_ban_observed',
      'qy_vio_ban_policy',
      'qy_vio_policy_default_row',
      'qy_vio_policy_badge_undeletable',
      'qy_vio_policy_action_record',
      'qy_vio_policy_action_restrict',
      'qy_vio_policy_action_ban',
      'qy_vio_policy_action_record_desc',
      'qy_vio_policy_action_restrict_desc',
      'qy_vio_policy_action_ban_desc',
      'qy_vio_policy_impact_title',
      'qy_vio_policy_impact_count',
      'qy_vio_policy_impact_failed',
      'qy_vio_policy_confirm_apply',
      'qy_vio_policy_confirm_warning',
      'qy_vio_policy_fallback_from_yaml',
      'qy_vio_policy_intro_disabled',
      'qy_vio_policy_intro_actions',
    ]
    for (const key of keys) {
      assert.ok(key in zh, `zh.json 缺少 ${key}`)
      assert.ok(key in en, `en.json 缺少 ${key}`)
    }
  })

  test('三档动作的说明必须点破「限制与封号是同一个账号状态」', () => {
    // 主库只有一个非删除停用态。不写出来的话，界面看起来像有两种账号状态，
    // 管理员会以为「限制」是一个更轻的处罚 —— 实际上 relay 一样是 403。
    const banDesc = (zh as Record<string, string>)[
      'qy_vio_policy_action_ban_desc'
    ]
    assert.ok(
      banDesc.includes('会话'),
      '封号档的说明必须点明它与「限制账号」的唯一差别是吊销会话'
    )
    const intro = (zh as Record<string, string>)['qy_vio_policy_intro_actions']
    assert.ok(intro.includes('403'), '列表页顶部的说明要讲清受限账号还能做什么')
  })
})
