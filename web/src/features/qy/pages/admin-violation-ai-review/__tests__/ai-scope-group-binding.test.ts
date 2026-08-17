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

import { qyAiScopeGroupBindingError, qyAiScopeToDraft } from '../lib/ai-review'

/**
 * ai-scope-group-binding.test.ts —— 「一条策略必须绑定分组」的前端那一半。
 *
 * 项目方原话:「强制绑定分组,全站模型还是太高了一点」。覆盖全站有两种写法,
 * 而只挡一种等于没挡:
 *
 *	名单为空    匹配全部分组。
 *	排除方向    匹配名单之外的全部分组,而且随着新分组的建立自动变宽。
 *
 * 这里钉的是**判据本身**;它接进保存键与那两句提示由
 * `ai-scope-card.test.tsx` 在真组件上钉住。
 */

const base = (over: Partial<ReturnType<typeof qyAiScopeToDraft>> = {}) => ({
  ...qyAiScopeToDraft(),
  name: '自助注册',
  enabled: true,
  group_scope: 'selfserve',
  ...over,
})

describe('分组绑定校验', () => {
  const cases: {
    name: string
    draft: ReturnType<typeof base>
    want: 'empty' | 'exclude' | null
    why: string
  }[] = [
    {
      name: '绑好分组的启用策略:通过',
      draft: base(),
      want: null,
      why: '这是唯一一种应该被放行的形状',
    },
    {
      name: '分组为空:拦',
      draft: base({ group_scope: '' }),
      want: 'empty',
      why: '空名单 = 全站,这一档的抽样率会作用在所有用户身上',
    },
    {
      name: '分组只有空白:同样拦',
      draft: base({ group_scope: '  \n ' }),
      want: 'empty',
      why: '只挡空串不挡空白,等于留了一个按几下空格就能绕过的闸',
    },
    {
      name: '停用的草稿分组也为空:仍然拦',
      draft: base({ enabled: false, group_scope: '' }),
      want: 'empty',
      why:
        '这一格在界面上是必填项:一条不绑分组的策略永远开不起来,' +
        '让人先存下再发现开不了,等于把错误推迟到最不该发现它的时刻',
    },
    {
      name: '启用 + 排除方向:拦',
      draft: base({ group_scope_mode: 'exclude' }),
      want: 'exclude',
      why: '排除 = 名单之外的全部分组,与留空是同一件事',
    },
    {
      name: '停用 + 排除方向:放行',
      draft: base({ enabled: false, group_scope_mode: 'exclude' }),
      want: null,
      why:
        '存量里可能躺着一条 exclude 策略,它的修法之一是"先关掉再说" —— ' +
        '编辑态无条件拦会让人连备注都改不了。真正的闸在启用那一刻,后端同位置',
    },
  ]

  for (const tc of cases) {
    test(tc.name, () => {
      assert.equal(qyAiScopeGroupBindingError(tc.draft), tc.want, tc.why)
    })
  }

  test('新建出来的草稿一开始就是不可保存的', () => {
    // 分组是空的,所以弹窗一开保存键就该是灰的。这一条把"默认值"与"校验"
    // 绑在一起:哪天默认草稿改成预填某个分组,这里会立刻红。
    assert.equal(qyAiScopeGroupBindingError(qyAiScopeToDraft()), 'empty')
  })
})

describe('提示文案两侧都在', () => {
  const enKeys = en as Record<string, string>
  const zhKeys = zh as Record<string, string>

  test('必填、排除被拒、以及存量未绑定那两句', () => {
    // 这几条都是"为什么不让我保存 / 这一行怎么了"的唯一答案。缺一个的表现是
    // 界面上直接显示原始键名,而人正卡在一个点不动的保存键前面。
    for (const key of [
      'qy_ai_f_required',
      'qy_ai_scope_group_required',
      'qy_ai_scope_mode_exclude_blocked',
      'qy_ai_scope_unbound_active',
      'qy_ai_scope_unbound_idle',
    ]) {
      assert.ok(key in zhKeys, `zh.json 缺少 ${key}`)
      assert.ok(key in enKeys, `en.json 缺少 ${key}`)
    }
  })

  test('分组与方向那两句 hint 不再说"留空表示全部分组"', () => {
    // 留着那句话比没有提示更糟:它教的正是现在会被拒的那种配法,
    // 而人照做之后收到的是一句他觉得自相矛盾的报错。
    assert.ok(!zhKeys.qy_ai_scope_f_groups_hint?.includes('留空表示全部分组'))
    assert.ok(
      !enKeys.qy_ai_scope_f_groups_hint?.includes('empty means all groups')
    )
  })
})
