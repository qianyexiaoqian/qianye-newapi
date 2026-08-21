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
 * `ccswitch://` 导入链接里的 `endpoint`。
 *
 * # 守什么
 *
 * Codex 要的是完整的 `/v1` 端点，Claude / Gemini 要的是基址。这一段原本是
 * 裸拼 `${base}/v1`，于是运营把线路配成 `https://a.com/v1`（地址簿允许，
 * 后端只归一化尾斜杠、不碰路径）时会导出一个 `.../v1/v1` 的端点。
 * 那条链接照样能生成、照样能导进客户端，只有真正发请求时才 404 ——
 * 用户会把这笔账记在站点头上。
 *
 * 现在它与密钥页那两个复制按钮共用同一条规则（`qyApiBaseWithV1`）。这条
 * 测试守的正是"共用"这件事：谁把它改回裸拼，这里就红。
 *
 * `homepage` 不跟着线路走这一条由 `api-address-picker` 的流程测试守，
 * 这里不重复。
 */
import assert from 'node:assert/strict'
import { before, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import { buildCCSwitchURL } from '@/features/keys/lib/cc-switch-url'

/*
 * 被测函数住在 `features/keys/lib`，测试却放在这里：`src/features/keys` 那个
 * 跑测单元已经在 bun 的 node:test 垫片下整批塌掉（同目录里有异步 React 测试，
 * 后续文件的顶层 describe 会被当成"嵌在别人的 test 里"，见
 * scripts/run-tests.mjs 与 KNOWN_FAILURES 里登记的 8 条）。往那儿再放一个
 * 文件只会让基线多一条假失败。而 `/v1` 这条规则本来就归 api-address-picker
 * 这个目录管，测试跟着规则走。
 *
 * 顶层不用 `await import(...)`：`readSiteServerAddress` 是在**调用时**才读
 * localStorage 与 window，静态 import 之后再装 DOM 全局完全来得及。
 */
const domWindow = new Window({ width: 1024, height: 768 })
for (const key of ['window', 'localStorage'] as const) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const SITE = 'https://site.example.com'

before(() => {
  localStorage.setItem('status', JSON.stringify({ server_address: SITE }))
})

function endpointOf(app: string, apiAddress: string): string {
  const url = buildCCSwitchURL(app, 'name', { model: 'm' }, 'sk-x', apiAddress)
  const params = new URLSearchParams(url.slice(url.indexOf('?') + 1))
  const endpoint = params.get('endpoint')
  assert.ok(endpoint != null, '导入链接里没有 endpoint')
  return endpoint
}

describe('CC Switch 导入链接里的 endpoint', () => {
  const cases: { app: string; address: string; expect: string; why: string }[] =
    [
      {
        app: 'codex',
        address: 'https://line.example.com',
        expect: 'https://line.example.com/v1',
        why: 'Codex 要完整的 /v1 端点',
      },
      {
        app: 'codex',
        address: 'https://line.example.com/',
        expect: 'https://line.example.com/v1',
        why: '线路带尾斜杠时不能拼成 //v1',
      },
      {
        app: 'codex',
        address: 'https://line.example.com/v1',
        expect: 'https://line.example.com/v1',
        why: '线路本来就配到了 /v1，绝不能拼成 /v1/v1',
      },
      {
        app: 'claude',
        address: 'https://line.example.com/',
        expect: 'https://line.example.com',
        why: 'Claude 要基址，尾斜杠也要去掉',
      },
      {
        app: 'gemini',
        address: 'https://line.example.com/v1',
        expect: 'https://line.example.com/v1',
        why: '非 Codex 不动路径，运营配成什么就是什么',
      },
      {
        app: 'codex',
        address: '',
        expect: `${SITE}/v1`,
        why: '没走线路选择时回落到站点地址，同样要按规则补 /v1',
      },
    ]

  for (const item of cases) {
    test(`${item.app} + "${item.address || '(回落站点)'}" — ${item.why}`, () => {
      assert.equal(endpointOf(item.app, item.address), item.expect, item.why)
    })
  }
})
