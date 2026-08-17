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
 * 封面来源 → `<img src>` 的那一次翻译。
 *
 * 这一格只有三个分支，但每一个错掉的后果都是**看得见的破图**：
 *   · 站内引用拼错路径 → 大厅每张卡都是碎图标（而路径是纯字符串，没有类型护栏）
 *   · 外链协议不判     → `javascript:` 之类的东西被原样交给浏览器
 *   · 空值不回 null    → 渲染出 `src=""`，多数浏览器会立刻请求当前页面并画破图
 *
 * 变异验证见文件末尾。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { qyLotCoverKind, qyLotCoverSrc } from '../cover'

describe('活动封面的来源翻译', () => {
  test('上传引用拼成站内匿名端点的路径', () => {
    assert.equal(
      qyLotCoverSrc({ cover_ref: 'abc123' }),
      '/api/qy/lottery/covers/abc123'
    )
    // 路径段必须编码。`ref` 来自接口响应，把一段未编码的字符串拼进 URL 路径
    // 是这一整类问题里最便宜的一个洞。
    assert.equal(
      qyLotCoverSrc({ cover_ref: 'a/b?c' }),
      '/api/qy/lottery/covers/a%2Fb%3Fc'
    )
  })

  test('两种来源同时非空时以站内那一份为准', () => {
    // 后端保证互斥，但库里那两列可以被手工改坏，而"站内的那一份"是两者中
    // 唯一可验证的来源。
    assert.equal(
      qyLotCoverSrc({ cover_ref: 'abc123', cover_url: 'https://evil.test/x' }),
      '/api/qy/lottery/covers/abc123'
    )
    assert.equal(
      qyLotCoverKind({ cover_ref: 'abc123', cover_url: 'https://a.test/x' }),
      'upload'
    )
  })

  test('外链只放行 http / https', () => {
    assert.equal(
      qyLotCoverSrc({ cover_url: 'https://cdn.test/a.png' }),
      'https://cdn.test/a.png'
    )
    assert.equal(
      qyLotCoverSrc({ cover_url: ' http://cdn.test/a.png ' }),
      'http://cdn.test/a.png'
    )
    for (const bad of [
      'javascript:alert(1)',
      'data:image/png;base64,iVBORw0KGgo=',
      'file:///etc/passwd',
      '//cdn.test/a.png',
      'cdn.test/a.png',
    ]) {
      assert.equal(qyLotCoverSrc({ cover_url: bad }), null, bad)
    }
  })

  test('没配封面回 null 而不是空串', () => {
    // 空串会渲染出 `src=""`：多数浏览器会立刻发一次指向当前页面的请求，
    // 然后画一个破图图标 —— 恰好是这条需求要求避免的那个东西。
    assert.equal(qyLotCoverSrc({}), null)
    assert.equal(qyLotCoverSrc({ cover_ref: '', cover_url: '   ' }), null)
    assert.equal(qyLotCoverKind({ cover_ref: '', cover_url: '' }), 'none')
  })

  test('来源分类决定要不要挂 no-referrer', () => {
    // 外链指向管理员随手填的第三方主机。分类错了，每一位访客的来源地址
    // 都会白送给那台机器。
    assert.equal(qyLotCoverKind({ cover_url: 'https://cdn.test/a.png' }), 'link')
    assert.equal(qyLotCoverKind({ cover_ref: 'abc' }), 'upload')
  })
})

/*
 * ── 变异验证（手工执行并已回滚）──
 *
 *   qyLotCoverSrc 去掉 encodeURIComponent      → "路径段必须编码" 红
 *   qyLotCoverSrc 去掉 /^https?:\/\//i 判定    → "外链只放行 http / https" 红
 *   qyLotCoverSrc 空值回 '' 而不是 null        → "没配封面回 null" 红
 *   qyLotCoverSrc 把外链分支排在上传引用之前   → "以站内那一份为准" 红
 *   qyLotCoverKind 把 link/upload 判定对调     → 最后两条同时红
 */
