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
import { describe, test } from 'node:test'

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import {
  qyAiScopeAudience,
  qyAiScopeChannelState,
  qyAiScopeDraftToInput,
  qyAiScopeHasFakeSeparator,
  qyAiScopeRowKind,
  qyAiScopeToDraft,
  qyAiSplitScopeList,
} from '../lib/ai-review'
import type { QyAiScope, QyAiScopeSummaryRow } from '../types'

/**
 * ai-scope.test.ts —— AI 审核作用域策略的前端回归。
 *
 * 四件事必须钉死,每一件都对应一种"界面上配好了、线上不是那么回事":
 *
 *  1. **抽样率的换算与解析失败的方向**。解析失败必须落到 0(不花钱、
 *     不把用户内容发往第三方),绝不能落到全量送审。
 *  2. **一行汇总的定性顺序**。被遮住优先于免审 —— 一条被遮住的策略哪怕
 *     写着 50%,真实抽样率也是 0,先报"免审"会把人引去改错的那一格。
 *  3. **名单的切分口径与后端逐字一致**。多认一个分隔符就会让界面显示成
 *     两个分组、后端只认出一个,而这条策略从此永远匹配不到任何人。
 *  4. **拼出来的 i18n 键**。`qy_ai_scope_kind_${kind}` 这一族抓不到,
 *     漏一个就会在表格里直接显示原始键名。
 */

function summaryRow(
  over: Partial<QyAiScopeSummaryRow> = {}
): QyAiScopeSummaryRow {
  return {
    id: 1,
    name: '高风险',
    enabled: true,
    priority: 100,
    model_scope: '',
    group_scope: 'selfserve',
    group_scope_mode: 'include',
    pre_sample_rate_bps: 5000,
    async_sample_rate_bps: 5000,
    prompt_source: 'inherit',
    category_id: 0,
    channel_id: 0,
    shadowed: false,
    ...over,
  }
}

describe('作用域草稿与请求体', () => {
  test('新建默认停用 —— 一条策略保存即可能改变谁的内容被发往第三方', () => {
    const draft = qyAiScopeToDraft()
    assert.equal(draft.enabled, false)
    assert.equal(draft.prePercent, '0')
    assert.equal(draft.asyncPercent, '0')
    assert.equal(draft.group_scope_mode, 'include')
  })

  test('从已有策略回填时百分比按万分比换算', () => {
    const row: QyAiScope = {
      id: 7,
      name: '自助注册',
      enabled: true,
      priority: 10,
      model_scope: 'gpt-4*',
      group_scope: 'selfserve',
      group_scope_mode: 'include',
      pre_sample_rate_bps: 5000,
      async_sample_rate_bps: 1000,
      prompt: '',
      category_id: 0,
      channel_id: 0,
      remark: '',
      created_at: 0,
      updated_at: 0,
    }
    const draft = qyAiScopeToDraft(row)
    assert.equal(draft.prePercent, '50')
    assert.equal(draft.asyncPercent, '10')
    assert.equal(draft.id, 7)
  })

  test('往返一圈不改变任何取值', () => {
    const row: QyAiScope = {
      id: 7,
      name: '自助注册',
      enabled: true,
      priority: 10,
      model_scope: 'gpt-4*',
      group_scope: 'selfserve',
      group_scope_mode: 'exclude',
      pre_sample_rate_bps: 250,
      async_sample_rate_bps: 10000,
      prompt: '本档判定说明',
      category_id: 12,
      channel_id: 4,
      remark: '备注',
      created_at: 0,
      updated_at: 0,
    }
    const back = qyAiScopeDraftToInput(qyAiScopeToDraft(row))
    assert.equal(back.pre_sample_rate_bps, 250)
    assert.equal(back.async_sample_rate_bps, 10000)
    assert.equal(back.group_scope_mode, 'exclude')
    assert.equal(back.id, 7)
    // 提示词与类型绑定同样要原样往返：往返丢字段的表现是"编辑一下抽样率就把
    // 这一档的提示词清空了"，而清空之后它会静默回到继承全局。
    assert.equal(back.prompt, '本档判定说明')
    assert.equal(back.category_id, 12)
    // 指定渠道同理:往返丢掉它 = 这一档静默回到「按权重随机」,
    // 于是用户内容开始被发去运营明确没有选的端点,而界面上什么都没变。
    assert.equal(back.channel_id, 4)
  })

  test('抽样率解析失败一律落到 0,绝不落到全量送审', () => {
    const draft = qyAiScopeToDraft()
    for (const bad of ['', '  ', 'abc', '-5', 'NaN']) {
      const body = qyAiScopeDraftToInput({
        ...draft,
        prePercent: bad,
        asyncPercent: bad,
      })
      assert.equal(body.pre_sample_rate_bps, 0, `"${bad}" 必须解析成 0`)
      assert.equal(body.async_sample_rate_bps, 0, `"${bad}" 必须解析成 0`)
    }
  })

  test('超过 100% 先在界面上夹住,而不是等后端 400', () => {
    const body = qyAiScopeDraftToInput({
      ...qyAiScopeToDraft(),
      prePercent: '250',
      asyncPercent: '999',
    })
    assert.equal(body.pre_sample_rate_bps, 10000)
    assert.equal(body.async_sample_rate_bps, 10000)
  })

  test('名称与作用域两侧的空白在提交前去掉', () => {
    const body = qyAiScopeDraftToInput({
      ...qyAiScopeToDraft(),
      name: '  自助注册  ',
      group_scope: '  selfserve  ',
      model_scope: '  gpt-4*  ',
    })
    assert.equal(body.name, '自助注册')
    assert.equal(body.group_scope, 'selfserve')
    assert.equal(body.model_scope, 'gpt-4*')
  })
})

describe('汇总行的定性', () => {
  const cases: { name: string; row: QyAiScopeSummaryRow; want: string }[] = [
    { name: '正常监控', row: summaryRow(), want: 'active' },
    {
      name: '两个抽样率都是 0 是免审名单,不是"没配"',
      row: summaryRow({ pre_sample_rate_bps: 0, async_sample_rate_bps: 0 }),
      want: 'exempt',
    },
    {
      name: '停用的档',
      row: summaryRow({ enabled: false }),
      want: 'disabled',
    },
    {
      name: '被遮住优先于免审 —— 真正要改的是优先级,不是抽样率',
      row: summaryRow({
        shadowed: true,
        pre_sample_rate_bps: 0,
        async_sample_rate_bps: 0,
      }),
      want: 'shadowed',
    },
    {
      name: '被遮住优先于停用',
      row: summaryRow({ shadowed: true, enabled: false }),
      want: 'shadowed',
    },
    {
      name: '只开转发后也算监控中',
      row: summaryRow({ pre_sample_rate_bps: 0, async_sample_rate_bps: 100 }),
      want: 'active',
    },
  ]
  for (const c of cases) {
    test(c.name, () => {
      assert.equal(qyAiScopeRowKind(c.row), c.want)
    })
  }

  test('指定的渠道坏了:抽样照跑,但每次都是「无可用渠道」+ 放行', () => {
    assert.equal(
      qyAiScopeRowKind(summaryRow({ channel_id: 9 }), { channelBroken: true }),
      'channel_down',
      '这一档实际上已经不审核任何内容,而它在列表上与正常策略长得一模一样'
    )
  })

  test('免审排在渠道坏掉之前 —— 两个抽样率都是 0 时渠道是什么状态都无关', () => {
    assert.equal(
      qyAiScopeRowKind(
        summaryRow({
          pre_sample_rate_bps: 0,
          async_sample_rate_bps: 0,
          channel_id: 9,
        }),
        { channelBroken: true }
      ),
      'exempt',
      '报"渠道不可用"会把人引去修一个根本不影响结果的东西'
    )
  })
})

describe('指定审核渠道这一格的定性', () => {
  const channels = [
    { id: 1, enabled: true },
    { id: 2, enabled: false },
  ]
  const cases: {
    name: string
    channelId: number
    channels: { id: number; enabled: boolean }[]
    want: string
  }[] = [
    { name: '0 = 不指定,按权重随机', channelId: 0, channels, want: 'default' },
    { name: '指定一个启用中的渠道', channelId: 1, channels, want: 'ok' },
    {
      name: '指定的渠道被停用了 —— 这一档已经不再审核任何内容',
      channelId: 2,
      channels,
      want: 'disabled',
    },
    {
      name: '指定的渠道被删了',
      channelId: 99,
      channels,
      want: 'missing',
    },
    {
      name: '渠道清单拉不到时报 missing,而不是把一条停止工作的策略画成正常的',
      channelId: 1,
      channels: [],
      want: 'missing',
    },
  ]
  for (const c of cases) {
    test(c.name, () => {
      assert.equal(qyAiScopeChannelState(c.channelId, c.channels), c.want)
    })
  }
})

describe('监控范围的描述', () => {
  test('分组留空 = 全部分组,此时方向不起作用', () => {
    const aud = qyAiScopeAudience(
      summaryRow({ group_scope: '', group_scope_mode: 'exclude' })
    )
    assert.equal(aud.allGroups, true)
    assert.equal(
      aud.exclude,
      false,
      '名单为空时后端根本不看方向,界面显示"排除(空名单)"会让人以为它排除了什么'
    )
  })

  test('exclude 方向被如实报出', () => {
    const aud = qyAiScopeAudience(
      summaryRow({ group_scope: 'internal,batch', group_scope_mode: 'exclude' })
    )
    assert.equal(aud.exclude, true)
    assert.deepEqual(aud.groups, ['internal', 'batch'])
  })

  test('模型留空 = 全部模型', () => {
    assert.equal(qyAiScopeAudience(summaryRow()).allModels, true)
    assert.equal(
      qyAiScopeAudience(summaryRow({ model_scope: 'gpt-4*' })).allModels,
      false
    )
  })
})

describe('名单切分与后端同口径', () => {
  test('半角逗号、换行、回车是分隔符,两侧空白去掉', () => {
    assert.deepEqual(qyAiSplitScopeList('vip, svip\nbatch\r\n internal '), [
      'vip',
      'svip',
      'batch',
      'internal',
    ])
  })

  test('空串与全空白得到空数组', () => {
    assert.deepEqual(qyAiSplitScopeList(''), [])
    assert.deepEqual(qyAiSplitScopeList('  ,  ,\n'), [])
  })

  test('全角逗号不是分隔符 —— 后端 splitList 不认它', () => {
    assert.deepEqual(
      qyAiSplitScopeList('vip，svip'),
      ['vip，svip'],
      '多认一个分隔符,界面会显示成两个分组而后端只认出一个,这条策略从此永远匹配不到'
    )
  })

  test('看起来像分隔符的字符会被当场标出来', () => {
    assert.equal(qyAiScopeHasFakeSeparator('vip，svip'), true)
    assert.equal(qyAiScopeHasFakeSeparator('vip、svip'), true)
    assert.equal(qyAiScopeHasFakeSeparator('vip;svip'), true)
    assert.equal(qyAiScopeHasFakeSeparator('vip,svip'), false)
    assert.equal(qyAiScopeHasFakeSeparator('vip\nsvip'), false)
  })
})

describe('拼出来的 i18n 键两侧都在', () => {
  const enKeys = en as Record<string, string>
  const zhKeys = zh as Record<string, string>

  test('四种状态与两个方向', () => {
    // 这一族是 `qy_ai_scope_kind_${kind}` / `qy_ai_scope_mode_${m}` 拼出来的,
    // 通用的"扫源码找 qy_ai_ 键"那条测试抓不到。漏一个的表现是表格里
    // 直接显示原始键名 —— 而"永远匹配不到"那一格恰恰是最需要看懂的。
    for (const key of [
      'qy_ai_scope_kind_active',
      'qy_ai_scope_kind_exempt',
      'qy_ai_scope_kind_disabled',
      'qy_ai_scope_kind_shadowed',
      'qy_ai_scope_kind_channel_down',
      'qy_ai_scope_mode_include',
      'qy_ai_scope_mode_exclude',
      // `qy_ai_scope_channel_${state}` 这一族同样是拼出来的。
      'qy_ai_scope_channel_disabled',
      'qy_ai_scope_channel_missing',
    ]) {
      assert.ok(key in zhKeys, `zh.json 缺少 ${key}`)
      assert.ok(key in enKeys, `en.json 缺少 ${key}`)
    }
  })
})
