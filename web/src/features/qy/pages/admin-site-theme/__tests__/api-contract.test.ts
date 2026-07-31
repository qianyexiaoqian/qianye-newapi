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
import { afterEach, describe, test } from 'node:test'

import { api } from '@/lib/api'

import { qySaveSiteTheme, qySiteThemeQuery } from '../api'
import type { QySiteThemeConfig } from '../types'

/**
 * 这一页曾经的缺陷正是「后端接口齐全，前端一行都没调」。因此这里保护的不是
 * 组件长什么样，而是**请求真的发到了那个后端路由、并且带着后端要的字段**。
 *
 * 做法是换掉 axios 适配器、跑真实的 `qyGet`/`qyPut` 代码路径（含
 * `QY_API_PREFIX` 拼接与信封解包），不 mock 被测模块自身。
 */

type Recorded = {
  method: string
  url: string
  data: unknown
}

const realAdapter = api.defaults.adapter

function captureRequest(responseData: unknown): Recorded[] {
  const calls: Recorded[] = []
  api.defaults.adapter = async (config) => {
    calls.push({
      method: (config.method ?? '').toLowerCase(),
      url: config.url ?? '',
      // axios 在适配器层拿到的是已序列化的字符串，反序列化后再断言，
      // 这样「字段被 JSON 丢掉」这类错误也能被抓住。
      data: typeof config.data === 'string' ? JSON.parse(config.data) : null,
    })
    return {
      data: { success: true, data: responseData },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    }
  }
  return calls
}

const serverConfig: QySiteThemeConfig = {
  default_preset: 'steins-gate',
  force_preset: true,
  allowed_presets: ['default', 'steins-gate'],
  upstream_default: 'default',
}

afterEach(() => {
  api.defaults.adapter = realAdapter
})

describe('site theme admin API wiring', () => {
  test('reading the config issues GET /api/qy/admin/site-theme and unwraps the envelope', async () => {
    const calls = captureRequest(serverConfig)

    const queryFn = qySiteThemeQuery().queryFn
    assert.equal(typeof queryFn, 'function')
    const result = await (queryFn as () => Promise<QySiteThemeConfig>)()

    assert.equal(calls.length, 1)
    assert.equal(calls[0].method, 'get')
    assert.equal(calls[0].url, '/api/qy/admin/site-theme')
    assert.deepEqual(result, serverConfig)
  })

  test('saving issues PUT /api/qy/admin/site-theme carrying both fields the handler binds', async () => {
    const calls = captureRequest({
      default_preset: 'ocean-breeze',
      force_preset: false,
    })

    await qySaveSiteTheme({
      default_preset: 'ocean-breeze',
      force_preset: false,
    })

    assert.equal(calls.length, 1)
    assert.equal(calls[0].method, 'put')
    assert.equal(calls[0].url, '/api/qy/admin/site-theme')
    // `force_preset: false` 必须原样出现。后端 `putSiteThemeRequest` 是全量覆盖
    // 语义，漏掉这个字段会被 Go 零值填成 false —— 恰好掩盖「关闭强制」这个 bug，
    // 却在开启强制时静默丢掉改动。
    assert.deepEqual(calls[0].data, {
      default_preset: 'ocean-breeze',
      force_preset: false,
    })
  })

  test('the query key stays under the qy prefix so a fund mutation can invalidate it wholesale', async () => {
    const key = qySiteThemeQuery().queryKey
    assert.equal(key[0], 'qy')
  })
})
