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
import { test } from 'node:test'

import { qyAddressBookAsApiInfo } from '../resolve-options'

// 密钥页右上角「API 地址」弹出列表原先只读上游「API 信息」配置,与页面左侧地址卡
// (二开地址簿)不是一份数据。现在地址簿优先,空数组 = 调用方回落上游配置。

test('maps address-book entries to the popover shape in operator order', () => {
  assert.deepEqual(
    qyAddressBookAsApiInfo([
      { id: 1, name: '无优化直连地址', remark: '', url: 'https://a.example' },
      { id: 2, name: '香港优化线路', remark: '大陆推荐', url: 'https://b.example' },
    ]),
    [
      { url: 'https://a.example', route: '无优化直连地址', description: '' },
      { url: 'https://b.example', route: '香港优化线路', description: '大陆推荐' },
    ]
  )
})

test('returns an empty list so the caller falls back to upstream api info', () => {
  assert.deepEqual(qyAddressBookAsApiInfo(undefined), [])
  assert.deepEqual(qyAddressBookAsApiInfo([]), [])
})

test('skips entries without a url', () => {
  assert.deepEqual(
    qyAddressBookAsApiInfo([
      { id: 1, name: '空地址', remark: '', url: '  ' },
      { id: 2, name: '有效', remark: '', url: 'https://ok.example' },
    ]),
    [{ url: 'https://ok.example', route: '有效', description: '' }]
  )
})
