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
 * 这组测试守的是「间歇性 500 错误页」的前端半边。
 *
 * 实测复现：把一个路由 chunk 的 URL 改成不存在的文件名后点导航，页面立刻变成
 * 项目方截图里的那一屏「500 糟糕！出错了 :')」。控制台是
 * `ChunkLoadError: Loading chunk 9243 failed` 和 `SyntaxError: Unexpected token '<'`，
 * 服务端 access log 里没有任何 5xx —— 那个 500 是 general-error.tsx 写死的兜底数字。
 *
 * 所以两条契约必须钉住：
 *   1. 构建产物过期这一类错误要能被认出来，页面才敢给出「重新加载」而不是「重试」；
 *   2. 认出来之后只允许自动重载一次，产物真的没了的时候不能变成刷新循环。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  claimStaleAssetReload,
  describeError,
  formatErrorReference,
  isStaleAssetError,
} from './error-diagnostics'

describe('isStaleAssetError', () => {
  test('认出重新构建后 chunk 失效的各种形态', () => {
    const staleAssetErrors: unknown[] = [
      // rspack/webpack：本次实测在浏览器里拿到的原样错误。
      Object.assign(new Error('Loading chunk 9243 failed.'), {
        name: 'ChunkLoadError',
      }),
      // name 被吞掉时只剩 message，同样要认出来。
      new Error(
        'Loading chunk 9243 failed.\n(missing: http://127.0.0.1:3011/static/js/async/9243.b5c19ac00c.js)'
      ),
      new Error('Loading CSS chunk 512 failed.'),
      // 原生 ESM 动态导入在各浏览器上的说法。
      new Error(
        'Failed to fetch dynamically imported module: /static/js/async/1.js'
      ),
      new Error('error loading dynamically imported module'),
      new Error('Importing a module script failed.'),
      // 服务端把缺失的 chunk 当 SPA 路由回了 index.html，浏览器按 JS 解析 HTML。
      new Error("Unexpected token '<'"),
    ]

    for (const error of staleAssetErrors) {
      assert.equal(isStaleAssetError(error), true, String(error))
    }
  })

  test('不把普通业务错误误判成版本过期 —— 误判会让页面自动刷新，把真实故障刷没', () => {
    const others: unknown[] = [
      new Error('Request failed with status code 500'),
      new Error('Network Error'),
      new Error('Cannot read properties of undefined (reading Chunk)'),
      { response: { status: 500 } },
      undefined,
      null,
      'Loading chunk 1 failed.',
    ]

    for (const error of others) {
      assert.equal(isStaleAssetError(error), false, String(error))
    }
  })
})

describe('describeError', () => {
  test('从 axios 错误里取出状态码、请求 ID 和失败的请求', () => {
    const diagnostics = describeError(
      Object.assign(new Error('Request failed with status code 500'), {
        config: { method: 'get', url: '/api/user/self' },
        response: {
          status: 500,
          headers: { 'X-Oneapi-Request-Id': '202608102038000000000abcdef' },
        },
      })
    )

    assert.equal(diagnostics.status, 500)
    assert.equal(diagnostics.requestId, '202608102038000000000abcdef')
    assert.equal(diagnostics.request, 'GET /api/user/self')
  })

  test('纯前端异常没有 HTTP 状态 —— 不能编一个出来', () => {
    const diagnostics = describeError(
      Object.assign(new Error('Loading chunk 9243 failed.'), {
        name: 'ChunkLoadError',
      })
    )

    assert.equal(diagnostics.status, undefined)
    assert.equal(diagnostics.requestId, undefined)
    assert.equal(diagnostics.request, undefined)
    assert.equal(diagnostics.name, 'ChunkLoadError')
  })

  test('可复制的那一行必须带上请求 ID，否则运维在服务端日志里搜不到', () => {
    const reference = formatErrorReference(
      describeError({
        name: 'AxiosError',
        message: 'Request failed with status code 500',
        config: {
          method: 'post',
          url: '/api/qy/lottery/activities/A1/entries',
        },
        response: {
          status: 500,
          headers: { 'x-oneapi-request-id': 'REQ-1' },
        },
      }),
      { path: '/qy/lottery', at: new Date('2026-08-10T12:34:56.000Z') }
    )

    assert.equal(
      reference,
      '2026-08-10T12:34:56.000Z | /qy/lottery | POST /api/qy/lottery/activities/A1/entries | HTTP 500 | rid=REQ-1 | AxiosError: Request failed with status code 500'
    )
  })
})

describe('claimStaleAssetReload', () => {
  function memoryStorage(initial?: string) {
    const store = new Map<string, string>()
    if (initial !== undefined)
      store.set('new-api:stale-asset-reload-at', initial)
    return {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => void store.set(key, value),
    }
  }

  test('第一次给额度，冷却期内不再给 —— 产物真缺失时不能刷新循环', () => {
    const storage = memoryStorage()
    const start = 1_754_800_000_000

    assert.equal(claimStaleAssetReload(storage, start), true)
    assert.equal(claimStaleAssetReload(storage, start + 1_000), false)
    assert.equal(claimStaleAssetReload(storage, start + 59_999), false)
    assert.equal(claimStaleAssetReload(storage, start + 60_000), true)
  })

  test('sessionStorage 不可用时不自动刷新，把处置权留给按钮', () => {
    assert.equal(claimStaleAssetReload(undefined, 1), false)
    assert.equal(
      claimStaleAssetReload(
        {
          getItem: () => {
            throw new Error('SecurityError')
          },
          setItem: () => {},
        },
        1
      ),
      false
    )
  })
})
