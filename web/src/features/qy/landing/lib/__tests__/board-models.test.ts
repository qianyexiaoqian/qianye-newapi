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
 * 信号板取哪几个模型名。
 *
 * 守的是「图文对得上」：首屏正文写着「Claude · Gemini · GPT 全系官方保真」，
 * 而 `/api/pricing` 的顺序是后台建渠道的顺序 —— 直接截前 N 条，板子上很可能
 * 一个 Claude 都没有。排序规则一旦被"顺手简化"成截断，页面不会报错、
 * 测试若只断言"有 N 行"也照样绿，只有这条会红。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { pickBoardModels } from '../board-models'

const named = (...names: string[]) =>
  names.map((model_name) => ({ model_name }))

describe('信号板的模型名挑选', () => {
  test('三支主推族排到前面，族内保持目录原有顺序', () => {
    const picked = pickBoardModels(
      named(
        'deepseek-v3',
        'gemini-2.5-pro',
        'qwen-max',
        'claude-sonnet-4-5',
        'gpt-5.1',
        'claude-opus-4-1'
      ),
      6
    )
    assert.deepEqual(picked, [
      'claude-sonnet-4-5',
      'claude-opus-4-1',
      'gemini-2.5-pro',
      'gpt-5.1',
      'deepseek-v3',
      'qwen-max',
    ])
  })

  test('族匹配不分大小写', () => {
    const picked = pickBoardModels(named('kimi-k2', 'Claude-3-7-Sonnet'), 2)
    assert.deepEqual(picked, ['Claude-3-7-Sonnet', 'kimi-k2'])
  })

  test('超出上限的部分被截掉，主推族不会因此被挤出去', () => {
    const picked = pickBoardModels(
      named('a-model', 'b-model', 'c-model', 'gpt-5.1'),
      2
    )
    assert.deepEqual(picked, ['gpt-5.1', 'a-model'])
  })

  test('重名只留一条', () => {
    const picked = pickBoardModels(named('gpt-5.1', 'gpt-5.1', 'kimi-k2'), 5)
    assert.deepEqual(picked, ['gpt-5.1', 'kimi-k2'])
  })

  test('空名与纯空白名不占行', () => {
    const picked = pickBoardModels(
      [
        { model_name: '' },
        { model_name: '   ' },
        {},
        { model_name: 'kimi-k2' },
      ],
      5
    )
    assert.deepEqual(picked, ['kimi-k2'])
  })

  test('目录取不到时返回空，交给调用方兜底而不是编造模型名', () => {
    assert.deepEqual(pickBoardModels(undefined, 7), [])
    assert.deepEqual(pickBoardModels([], 7), [])
  })

  test('上限为 0 或负数时不返回任何行', () => {
    assert.deepEqual(pickBoardModels(named('gpt-5.1'), 0), [])
    assert.deepEqual(pickBoardModels(named('gpt-5.1'), -1), [])
  })
})
