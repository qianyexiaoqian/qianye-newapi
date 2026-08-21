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
 * 「复制」与「带 V1 复制」这两个按钮到底复制出什么。
 *
 * # 为什么这条测试值得存在
 *
 * 两个按钮的产物是**字符串拼接**，而拼错的两种形态（`https://a.com//v1`、
 * `https://a.com/v1/v1`）在前端没有任何信号：typecheck 绿、页面正常渲染、
 * toast 说「已复制」。错误只在用户把它粘进客户端、跑出 404 之后才暴露，
 * 而那时他多半会怪站点而不是怪剪贴板。
 *
 * 用例表按「输入长什么样」分组，每一组都对应一种运营真的会填出来的东西：
 * 光域名、带端口、带路径前缀、带尾斜杠、本来就带了 /v1。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { qyApiBaseWithV1, qyNormalizeApiBase } from '../api-base-url'

type Case = {
  /** 这条用例守的是什么 —— 断言失败时打印它。 */
  why: string
  input: string
  /** 「复制」按钮复制出的东西。 */
  base: string
  /** 「带 V1 复制」按钮复制出的东西。 */
  v1: string
}

const CASES: Case[] = [
  {
    why: '最常见的一条：光域名',
    input: 'https://api.example.com',
    base: 'https://api.example.com',
    v1: 'https://api.example.com/v1',
  },
  {
    why: '尾斜杠不能拼出 //v1',
    input: 'https://api.example.com/',
    base: 'https://api.example.com',
    v1: 'https://api.example.com/v1',
  },
  {
    why: '手滑打出多个尾斜杠，一样只当作没有',
    input: 'https://api.example.com///',
    base: 'https://api.example.com',
    v1: 'https://api.example.com/v1',
  },
  {
    why: '前后空白（从别处粘过来的）',
    input: '  https://api.example.com/  ',
    base: 'https://api.example.com',
    v1: 'https://api.example.com/v1',
  },
  {
    why: '带端口',
    input: 'http://127.0.0.1:3011',
    base: 'http://127.0.0.1:3011',
    v1: 'http://127.0.0.1:3011/v1',
  },
  {
    why: '带端口又带尾斜杠',
    input: 'http://127.0.0.1:3011/',
    base: 'http://127.0.0.1:3011',
    v1: 'http://127.0.0.1:3011/v1',
  },
  {
    why: '反代挂在子路径下',
    input: 'https://example.com/newapi',
    base: 'https://example.com/newapi',
    v1: 'https://example.com/newapi/v1',
  },
  {
    why: '子路径 + 尾斜杠',
    input: 'https://example.com/newapi/',
    base: 'https://example.com/newapi',
    v1: 'https://example.com/newapi/v1',
  },
  {
    why: '运营本来就把 /v1 填进去了 —— 绝不能拼成 /v1/v1',
    input: 'https://api.example.com/v1',
    base: 'https://api.example.com/v1',
    v1: 'https://api.example.com/v1',
  },
  {
    why: '带 /v1 又带尾斜杠',
    input: 'https://api.example.com/v1/',
    base: 'https://api.example.com/v1',
    v1: 'https://api.example.com/v1',
  },
  {
    why: '子路径下的 /v1 同样算数（按段判，不是按整条路径判）',
    input: 'https://example.com/newapi/v1',
    base: 'https://example.com/newapi/v1',
    v1: 'https://example.com/newapi/v1',
  },
  {
    why: '/v1beta 不是 v1 端点：按段判，不能被后缀匹配蒙混过去',
    input: 'https://example.com/v1beta',
    base: 'https://example.com/v1beta',
    v1: 'https://example.com/v1beta/v1',
  },
  {
    why: '/v10 同理，不是 v1',
    input: 'https://example.com/v10',
    base: 'https://example.com/v10',
    v1: 'https://example.com/v10/v1',
  },
  {
    why: '主机名恰好叫 v1：末尾三个字符是 /v1，但那是主机不是路径',
    input: 'https://v1',
    base: 'https://v1',
    v1: 'https://v1/v1',
  },
  {
    why: '路径大小写敏感：/V1 不是本站挂的那个端点，不能当作已经带了 v1',
    input: 'https://example.com/V1',
    base: 'https://example.com/V1',
    v1: 'https://example.com/V1/v1',
  },
  {
    why: '空串（一条地址都取不到）：两个按钮都禁用，不复制空剪贴板',
    input: '',
    base: '',
    v1: '',
  },
  {
    why: '只有空白，与空串同解',
    input: '   ',
    base: '',
    v1: '',
  },
]

describe('API 地址的两条复制规则', () => {
  for (const item of CASES) {
    test(`${item.input || '(空)'} — ${item.why}`, () => {
      assert.equal(
        qyNormalizeApiBase(item.input),
        item.base,
        `「复制」复制出的地址不对：${item.why}`
      )
      assert.equal(
        qyApiBaseWithV1(item.input),
        item.v1,
        `「带 V1 复制」复制出的地址不对：${item.why}`
      )
    })
  }

  test('带 V1 是幂等的：再点一次也不会长出第二个 /v1', () => {
    for (const item of CASES) {
      assert.equal(
        qyApiBaseWithV1(qyApiBaseWithV1(item.input)),
        item.v1,
        `对 ${item.input} 连拼两次 /v1 长出了多余的段`
      )
    }
  })
})
