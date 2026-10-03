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
 * 首屏群聊卡片的取数判据。
 *
 * 这一份同时是两件事的守卫:
 *
 *   1. **首屏不开天窗**。`readQyGroupContact` 返回 null 时首屏回落到模型信号板,
 *      所以「没配」「配了但非法」「status 还没到」必须全部落到 null 这一条路上。
 *   2. **前后端判据同源**。样本集与 model/qy_home_group_option_test.go 逐条对齐 ——
 *      两侧分家就意味着后端存得下的值前端渲染不出来,或者反过来。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  readQyGroupContact,
  sanitizeQyGroupJoinUrl,
  sanitizeQyGroupNumber,
} from '../group-contact'

describe('qy 首屏群聊卡片的取数判据', () => {
  test('两项都拿不到时返回 null,首屏据此回落信号板', () => {
    assert.equal(readQyGroupContact(null), null)
    assert.equal(readQyGroupContact(undefined), null)
    assert.equal(readQyGroupContact({}), null)
    assert.equal(
      readQyGroupContact({
        qy_home_group_join_url: '',
        qy_home_group_number: '',
      }),
      null
    )
    // 字段类型不对(旧版快照、后端改过形状)也不能抛。
    assert.equal(
      readQyGroupContact({
        qy_home_group_join_url: 123,
        qy_home_group_number: null,
      }),
      null
    )
  })

  test('非法链接与没配走同一条兜底,不会渲染出半个空卡片', () => {
    assert.equal(
      readQyGroupContact({ qy_home_group_join_url: 'javascript:alert(1)' }),
      null
    )
    // 半配不互相拖累:链接非法但群号合法,仍然出卡,只是没有二维码。
    assert.deepEqual(
      readQyGroupContact({
        qy_home_group_join_url: 'javascript:alert(1)',
        qy_home_group_number: '1013106587',
      }),
      { joinUrl: '', number: '1013106587' }
    )
  })

  test('链接:放行 http/https 与带 query 的邀请链接', () => {
    // 群邀请链接几乎一定带 query。日后有人想把判据收紧成禁 query,这条会拦住他。
    assert.equal(
      sanitizeQyGroupJoinUrl('https://qm.qq.com/q/abc?k=xyz'),
      'https://qm.qq.com/q/abc?k=xyz'
    )
    assert.equal(
      sanitizeQyGroupJoinUrl('  http://10.0.0.5:8080/join  '),
      'http://10.0.0.5:8080/join'
    )
  })

  test('链接:伪协议、缺协议头、超长、不可见字符一律丢掉', () => {
    for (const bad of [
      'javascript:alert(1)',
      'JaVaScript:alert(1)',
      'data:text/html;base64,PHM+',
      'qm.qq.com/q/abc',
      '/join',
      'http://',
      '',
      '   ',
    ]) {
      assert.equal(sanitizeQyGroupJoinUrl(bad), '', bad)
    }
    // 超长这条守的是「一个配置值把整个前端外壳换成错误页」:编码器在超出容量时
    // 抛的是渲染期异常。挡在数据入口,那条退化路径就不存在了。
    assert.equal(sanitizeQyGroupJoinUrl('https://x.com/' + 'a'.repeat(600)), '')
    // new URL() 会静默剥掉换行并判为合法,这条显式拒绝把前端补齐到后端的严格度。
    assert.equal(sanitizeQyGroupJoinUrl('https://qm.qq.com/\nabc'), '')
    assert.equal(sanitizeQyGroupJoinUrl('https://qm.qq.com/\u202eabc'), '')
  })

  test('群号:放行数字与非数字标识,拒绝空格与整句说明', () => {
    // 运营粘贴常带空格。
    assert.equal(sanitizeQyGroupNumber('  1013106587  '), '1013106587')
    assert.equal(sanitizeQyGroupNumber('qianye_group-01'), 'qianye_group-01')

    for (const bad of [
      'ab',
      '1'.repeat(33),
      '101 310',
      '浅夜群 1013106587',
      '１０１３',
      '',
    ]) {
      assert.equal(sanitizeQyGroupNumber(bad), '', bad)
    }
  })
})
