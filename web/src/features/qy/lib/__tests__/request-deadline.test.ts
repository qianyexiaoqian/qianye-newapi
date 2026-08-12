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
import { createServer, type Server } from 'node:http'
import { after, before, describe, test } from 'node:test'

import { api } from '@/lib/api'

import { isQyError, qyGet } from '../api'

/**
 * qy 请求必须有**落定期限**。
 *
 * 这一条守的是违规类型页永久转圈那个缺陷。形状是:一个「发出去但永远收不到
 * 响应」的 GET,在 XHR 默认 timeout=0 之下永远不 reject,于是 React Query 里
 * 那个 query 永远停在 fetching —— 而这个状态是自锁的(Query.fetch() 直接返回
 * 那条死 promise、optionalRemove() 又不回收),重新挂载 / invalidate / refetch
 * 全部无效,页面只剩一个转不完的圈。
 *
 * 两条断言合起来才是完整的不变量,单独任何一条都有缺口:
 *
 *  1. **机制**:起一个接受连接却永不应答的服务端,跑真实 http 适配器,超时确实
 *     会打断请求并被归到 network 档 —— 也就是 QyPageBoundary 会渲染成错误态
 *     加重试按钮,而不是转圈。这一条自带 timeout 覆盖,只为不跑满 30s。
 *  2. **生产默认值**:调用方什么都不传时,到达 axios 的 timeout 必须是正的有限
 *     值。把 `QY_BASE_CONFIG.timeout` 删掉,这一条立刻红(实测报「实际:0」,
 *     0 正是 axios「无限等」的默认值,也正是线上那个缺陷的形状)。
 */

let server: Server
let baseURL = ''
/** 已经建立、但故意不回应的连接。测试结束要亲手拆,否则 server.close() 不返回。 */
const hung: Array<{ destroy: () => void }> = []

before(async () => {
  server = createServer((_req, res) => {
    // 刻意什么都不做:不写 body、不 end。连接就这么吊着。
    hung.push(res.socket ?? { destroy: () => {} })
  })
  await new Promise<void>((resolve) => {
    server.listen(0, '127.0.0.1', resolve)
  })
  const address = server.address()
  assert.ok(address != null && typeof address === 'object')
  baseURL = `http://127.0.0.1:${address.port}`
})

after(async () => {
  for (const socket of hung) socket.destroy()
  await new Promise<void>((resolve) => {
    server.close(() => resolve())
  })
})

describe('qy 请求的落定期限', () => {
  test('服务端永不应答时,qyGet 仍会在期限内以 network 失败落定', async () => {
    const started = Date.now()

    // 这一条验的是**机制**:真实 axios 适配器 + 一个永不应答的服务端,超时确实
    // 会把请求打断,并且被 toQyError 归到 network 档。期限在这里压到 800ms 只是
    // 为了不让单测跑满 30s —— 生产用的那个数由下一条断言钉住,两条合起来才是
    // 完整的不变量:「qy 请求一定会在有限时间内落定」。
    // 显式钉住真实的 http 适配器:`api` 是跨测试文件共享的模块单例,别的用例会
    // 把 `api.defaults.adapter` 换成 mock,不钉住的话这一条会拿到那个 mock 的
    // 秒回 200,于是「永不应答」这个前提根本没成立。
    const outcome = await qyGet('/violation/categories', undefined, {
      baseURL,
      timeout: 800,
      adapter: 'http',
    }).then(
      () => ({ settled: true, error: null as unknown }),
      (error: unknown) => ({ settled: true, error })
    )

    const elapsed = Date.now() - started

    assert.equal(outcome.settled, true, '请求必须落定,不能永远挂着')
    assert.ok(
      isQyError(outcome.error),
      `应当归一成 QyError,实际拿到:${String(outcome.error)}`
    )
    assert.equal(
      outcome.error.kind,
      'network',
      '收不到响应属于 network 档 —— QyPageBoundary 据此渲染错误态 + 重试按钮'
    )
    assert.ok(
      elapsed < 4_000,
      `必须在期限内落定,实际耗时 ${elapsed}ms —— 说明 timeout 没有生效`
    )
  })

  test('qy 的默认配置带着一个有限的超时', () => {
    // 上面那条自带 timeout 覆盖,所以它证明不了「生产默认值是有限的」。这一条
    // 专管这件事:调用方什么都不传时,到达 axios 的 timeout 必须是正的有限值。
    // 把 `QY_BASE_CONFIG.timeout` 删掉 / 改成 0,这条立刻红。
    const seen: Array<number | undefined> = []
    // 就地保存/还原,而不是用模块加载时的那一份 —— 同进程里别的测试文件也在换它。
    const previousAdapter = api.defaults.adapter
    api.defaults.adapter = async (config) => {
      seen.push(config.timeout)
      return {
        data: { success: true, data: { items: [] } },
        status: 200,
        statusText: 'OK',
        headers: {},
        config,
      }
    }

    // disableDuplicate 绕开在途去重表,这条断言就不依赖上一条留下的状态。
    return qyGet('/violation/categories', undefined, { disableDuplicate: true })
      .then(() => {
        assert.equal(seen.length, 1)
        const timeout = seen[0]
        assert.ok(
          typeof timeout === 'number' && timeout > 0 && Number.isFinite(timeout),
          `qy 请求必须带有限超时,实际:${String(timeout)}`
        )
      })
      .finally(() => {
        api.defaults.adapter = previousAdapter
      })
  })
})
