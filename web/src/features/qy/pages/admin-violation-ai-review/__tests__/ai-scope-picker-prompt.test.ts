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

import zh from '@/i18n/qy/zh.json'

import {
  qyNormalizeGroupName,
  qyUnknownGroupNames,
  type QyGroupOption,
} from '../../../lib/group-options'
import { qyAppendViolationGroupScope } from '../../admin-violation-rules/lib/rule-form'
import {
  qyAiAppendScopeGroup,
  qyAiScopeDraftToInput,
  qyAiScopeToDraft,
  qyAiSplitScopeList,
} from '../lib/ai-review'
import type { QyAiScope } from '../types'

/**
 * ai-scope-picker-prompt.test.ts —— 作用域这一档新加的三件事的前端回归。
 *
 * 三件事各对应项目方原话的一半:
 *
 *  1. 「分组点击自动拉取当前的模型分组」—— 下拉必须**复用**已有的那份共享
 *     清单,而不是这一页自己再拼一份取数;同时手输必须仍然可用。
 *  2. 「设置这个分组的AI审核提示词」—— 空 = 继承全局(**不是**"用内置默认"),
 *     而且不管用哪一档基底,类型清单都仍然自动拼进去。
 *  3. 「如果违规,记得也绑定一下违规类型」—— 类型绑定要能原样往返,
 *     0 是"不指定"这一档。
 *
 * 变异验证见文件末尾。
 */

const SRC = new URL('..', import.meta.url)
const read = (relative: string) => readFileSync(new URL(relative, SRC), 'utf-8')

const dict = zh as Record<string, string>

const OPTIONS: QyGroupOption[] = [
  { name: 'default', ratio: 1, has_channels: true, public_usable: true },
  { name: 'selfserve', ratio: 0.8, has_channels: true, public_usable: false },
]

// ─────────────────────── 一、分组选择器 ───────────────────────

describe('分组选择器复用的是共享清单,不是第二份实现', () => {
  /**
   * 直接读源码,比 TypeScript 强一层:下拉整块被删掉、或者被换成一个页面
   * 自己 fetch 的清单,类型检查一个错都不会报,而这一页反复出现的失败模式
   * 正是「实现齐全、消费层没接上」。
   */
  test('下拉的清单来自 features/qy/lib/group-options', () => {
    const page = read('index.tsx')
    assert.ok(
      page.includes("from '../../lib/group-options'"),
      '分组清单必须来自共享层,而不是这一页自己开一个取数'
    )
    assert.ok(
      page.includes('qyGroupOptionsQuery'),
      '必须用共享的分组候选查询 —— 同一份事实开两个来源,迟早会出现两页各自认为对方是错的'
    )
    // 锚在 **JSX 标签**上而不是标识符上:只 grep `ComboboxInput` 的话,
    // 把整块下拉从表单里删掉、只留下那一行 import,这条断言仍然是绿的。
    assert.ok(
      page.includes('<ComboboxInput'),
      '分组作用域必须真的渲染出可搜索的下拉,而不是继续让人手抄名字'
    )
    assert.ok(
      !page.includes('<datalist'),
      '裸 datalist 只提示、不校验、不告警,正是这一轮要换掉的东西'
    )
  })

  test('拉不到清单时不阻断:三种非正常状态各有一句话,且文本框仍在', () => {
    const page = read('index.tsx')
    for (const key of [
      'qy_ai_scope_group_loading',
      'qy_ai_scope_group_failed',
      'qy_ai_scope_group_empty',
    ]) {
      assert.ok(
        page.includes(key),
        `缺少 ${key}:拉取失败会变成一个没有解释的空下拉`
      )
      assert.ok(dict[key], `${key} 没有中文文案`)
    }
    // 清单为空(拉取失败,或者站点真的一个分组都没定义)时不许标未定义分组:
    // 那会把每一个名字都标成黄的,是一片假警报 —— 而假警报比没有警报更糟。
    // 锚在**这道收敛本身**(`… === 0 ? []`)上,而不是光锚 `groupOptions.length
    // === 0`:后者在下面那句「站点还没有定义任何分组」的条件里也出现一次,
    // 于是把收敛删掉之后这条断言仍然是绿的。
    assert.match(
      page,
      /groupOptions\.length === 0\s*\?\s*\[\]/,
      '清单为空时必须收起未定义分组告警'
    )
    assert.deepEqual(
      qyUnknownGroupNames(['selfserve', 'slefserve'], []),
      ['selfserve', 'slefserve'],
      '这个函数本身不判断清单拉到没有,收敛必须发生在调用侧'
    )
  })

  test('未定义分组是软告警,不进任何校验闸', () => {
    const names = qyAiSplitScopeList('selfserve, slefserve').map(
      qyNormalizeGroupName
    )
    assert.deepEqual(qyUnknownGroupNames(names, OPTIONS), ['slefserve'])
    // 历史分组(倍率表里已删、users 里还有人挂着)恰恰是最需要被审的那批账号,
    // 所以它照样要能提交 —— 后端 validateAIScope 同样只看长度、不看存在性。
    const body = qyAiScopeDraftToInput({
      ...qyAiScopeToDraft(),
      name: '历史分组',
      group_scope: 'ghost-group',
    })
    assert.equal(body.group_scope, 'ghost-group')
  })
})

describe('追加一项时的分隔符与后端逐字一致', () => {
  /**
   * 后端 `splitList` 只认半角逗号与换行。划转那一侧的 `parseGroupList` 额外认
   * 分号 —— 跟错一边,一个名字里带分号的分组会在界面上被拆成两个(两个都标黄的
   * 假警报),后端存的却仍是原来那一个。
   */
  test('分号是分组名的一部分,不是分隔符', () => {
    assert.deepEqual(qyAiSplitScopeList('default, vip\nbatch'), [
      'default',
      'vip',
      'batch',
    ])
    assert.deepEqual(qyAiSplitScopeList('a;b, c'), ['a;b', 'c'])
    assert.deepEqual(qyAiSplitScopeList('  ,, '), [])
  })

  test('追加时按归一后的名字去重,与违规规则页同口径', () => {
    assert.equal(qyAiAppendScopeGroup('VIP', 'vip'), 'VIP')
    assert.equal(qyAiAppendScopeGroup('vip', 'selfserve'), 'vip,selfserve')
    assert.equal(qyAiAppendScopeGroup('', 'selfserve'), 'selfserve')
    assert.equal(qyAiAppendScopeGroup('vip', '   '), 'vip')
    // 两页对同一份名单必须给出同一个结果:各写一份的表现是同一个分组名在一页
    // 被追加、在另一页被判成重复,而两页说的都是同一个后端口径。
    for (const [raw, pick] of [
      ['VIP', 'vip'],
      ['vip', 'selfserve'],
      ['a;b', 'c'],
      ['', 'vip'],
    ] as const) {
      assert.equal(
        qyAiAppendScopeGroup(raw, pick),
        qyAppendViolationGroupScope(raw, pick),
        `"${raw}" + "${pick}":AI 作用域与违规规则必须给出同一个名单`
      )
    }
  })
})

// ─────────────────────── 二、作用域提示词 ───────────────────────

/*
 * 「作用域提示词覆盖全局」这一组已经整体退役。
 *
 * 2026-09-06 提示词从「全局设置 / 作用域」搬到了**审核渠道**上,三档回落
 * 不再存在。理由是它与协议绑死:护栏协议压根不发提示词,而挂在作用域上就
 * 允许"一条作用域的提示词被分发到一个根本不读提示词的渠道"。
 *
 * 接替它的是:
 *   后端  aireview_scope_prompt_test.go 的 TestAIChannelPromptIsTheOnlySource
 *         与 TestAIChannelPromptReachesUpstreamRequest(逐渠道渲染)
 *   前端  本文件下面那一组「作用域选审核渠道分组」
 */

describe('作用域选的是审核渠道分组,不再是"全部启用渠道"', () => {
  test('草稿往返带着 channel_group', () => {
    const draft = qyAiScopeToDraft()
    draft.channel_group = '自建护栏'
    assert.equal(draft.channel_group, '自建护栏')
    assert.equal(qyAiScopeDraftToInput(draft).channel_group, '自建护栏')
  })

  test('分组名两侧的空白在提交前就被去掉', () => {
    // 不归一的话「自建护栏」与「自建护栏 」会是两个分组,而界面上一模一样。
    // 后端也会 trim 一次(它才是权威),这里折是为了让界面显示的与入库的一致。
    const draft = qyAiScopeToDraft()
    draft.channel_group = '  自建护栏  '
    assert.equal(qyAiScopeDraftToInput(draft).channel_group, '自建护栏')
  })

  test('新建时默认不选分组 —— 保存时由后端那道闸提醒', () => {
    // 前端不替运营猜一个分组:猜错的后果是用户内容发去了他没选过的端点。
    // 空着提交会被后端 400(启用中的策略必须选分组或指定渠道),那句话
    // 比一个悄悄填好的默认值有用得多。
    assert.equal(qyAiScopeToDraft().channel_group, '')
  })
})

describe('作用域绑定违规类型', () => {
  const row = (over: Partial<QyAiScope> = {}): QyAiScope => ({
    id: 7,
    name: '自助注册',
    enabled: true,
    priority: 10,
    model_scope: '',
    group_scope: 'selfserve',
    group_scope_mode: 'include',
    pre_sample_rate_bps: 0,
    async_sample_rate_bps: 5000,
    channel_group: '',
    category_id: 0,
    channel_ids: [],
    channel_mode: '',
    channel_failover: false,
    remark: '',
    created_at: 0,
    updated_at: 0,
    ...over,
  })

  test('0 是「不指定」,正数原样往返', () => {
    assert.equal(qyAiScopeToDraft(row()).category_id, 0)
    assert.equal(qyAiScopeToDraft(row({ category_id: 12 })).category_id, 12)
    assert.equal(
      qyAiScopeDraftToInput(qyAiScopeToDraft(row({ category_id: 12 })))
        .category_id,
      12
    )
  })

  /**
   * 负数与 NaN 一律折成 0(= 不指定)。
   *
   * 方向是刻意的:0 保持"按规则自己绑的类型记",与这一格出现之前完全一致。
   * 反过来(解析失败落到某个具体类型)会把一次手滑变成一批记错类型的违规记录,
   * 而类型计数是封号判据的一条线。
   */
  test('非法取值折成「不指定」,而不是折成某个类型', () => {
    for (const bad of [-1, Number.NaN, -0]) {
      const body = qyAiScopeDraftToInput({
        ...qyAiScopeToDraft(),
        name: 'x',
        category_id: bad,
      })
      assert.equal(body.category_id, 0, `${bad} 必须折成 0`)
    }
  })

  test('页面上摆出了「记为类型」这一列与「不指定」这一档', () => {
    const page = read('index.tsx')
    for (const key of [
      'qy_ai_scope_col_category',
      'qy_ai_scope_category_none',
      'qy_ai_scope_f_category',
      'qy_ai_scope_col_chgroup',
      'qy_ai_scope_f_chgroup',
    ]) {
      assert.ok(page.includes(key), `缺少 ${key}`)
      assert.ok(dict[key], `${key} 没有中文文案`)
    }
    // 类型清单复用违规类型页那个 query,不另开端点。
    assert.ok(
      page.includes('qyAdminViolationCategoriesQuery'),
      '违规类型清单必须复用已有的那个查询'
    )
    // 下拉里只放 id / name / is_fallback:整行 Category 里有内部备注与
    // 「给 AI 的判定说明」,两者都不该出现在一个"挑一个类型"的下拉里。
    assert.ok(
      !page.includes('row.category.remark') &&
        !page.includes('row.category.ai_guidance'),
      '类型下拉不许携带内部备注或判定说明'
    )
  })
})

/*
 * ── 变异验证(逐条改坏源码再跑本文件,改完即还原;括号里是实测结果)──
 *
 *  P1 `qyAiScopeEffectivePrompt` 去掉第一档(总是回落全局)
 *     → 「三档回落」红:`作用域写了 → 用它` 期望 "本档提示词" 实得 "本站全局提示词"
 *  P2 `qyAiScopePromptSource` 改成复用全局那一格的判定(把逐字等于内置默认也
 *     算成继承)→ 「逐字等于内置默认的作用域提示词是「自定义」」红
 *  P3 `qyAiScopeDraftToInput` 里 prompt 直接透传(不折空白)
 *     → 「只有空白的提示词提交时折成空串」红
 *  P4 `qyAiScopeToDraft` 改成预填 default_prompt
 *     → 「编辑框不预填」红
 *  P5 `QY_AI_SCOPE_SEPARATOR` 改成共享层那个(多认分号)
 *     → 「分号是分组名的一部分」红 + 「与违规规则页同口径」红
 *  P6 index.tsx 里把 ComboboxInput 整块删掉(只留文本框)
 *     → 「下拉的清单来自 features/qy/lib/group-options」红
 *  P7 `qyAiScopeDraftToInput` 里 category_id 直接透传
 *     → 「非法取值折成「不指定」」红(-1 原样出去,后端会 400,但界面上看不出来)
 *  P8 index.tsx 里删掉 `groupOptions.length === 0` 那道收敛
 *     → 「拉不到清单时不阻断」红
 */
