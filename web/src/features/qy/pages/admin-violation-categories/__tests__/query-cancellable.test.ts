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

import { QueryClient, QueryObserver } from '@tanstack/react-query'

import { api } from '@/lib/api'

import { qyAdminViolationCategoriesQuery } from '../api'

/**
 * 违规类型的 query 必须是**可取消的**。
 *
 * 这一条守的是「离开页面再回来仍然转圈」那一半缺陷。query-core 只在 queryFn
 * 真的读过 `context.signal` 时才置 `#abortSignalConsumed`;而最后一个 observer
 * 卸载时,只有这一位是真才会 `retryer.cancel({revert:true})` 把 fetchStatus 退回
 * idle,否则只调 `cancelRetry()` —— 它既不中断在途请求也不改 fetchStatus。
 *
 * 后果是可观测的:一次收不到响应的请求会把这个 query 永久钉在 fetching,而
 * `Query.fetch()` 在 fetchStatus!=='idle' 时直接返回那条死 promise、不再发请求,
 * 于是重新进页面、invalidate、refetch 全部无效。这个 key 又被违规类型页、
 * 违规规则页、AI 审核页三处共用,一次挂起污染三页。
 *
 * 断言的是行为而不是「queryFn 的形参长什么样」:把 queryFn 改回
 * `() => qyGet('/violation/categories')`(不读 signal),这条立刻变红。
 */

const realAdapter = api.defaults.adapter

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

afterEach(() => {
  api.defaults.adapter = realAdapter
})

describe('违规类型 query 的可取消性', () => {
  test('请求永不落定时,卸载最后一个 observer 会把 query 退回 idle', async () => {
    let sawSignal: unknown = null
    // 永不落定的适配器 —— 复刻「请求发出去了,响应永远不来」。
    api.defaults.adapter = (config) => {
      sawSignal = config.signal
      return new Promise(() => {})
    }

    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const options = qyAdminViolationCategoriesQuery()
    const observer = new QueryObserver(client, options)
    const unsubscribe = observer.subscribe(() => {})

    await sleep(50)

    const query = client.getQueryCache().find({ queryKey: options.queryKey })
    assert.ok(query != null, 'query 应当已经建立')
    assert.equal(
      query.state.fetchStatus,
      'fetching',
      '前置条件:请求应当正在飞'
    )
    assert.ok(
      sawSignal != null,
      'queryFn 必须读取并透传 signal —— 只有读过,query-core 才认为它可取消'
    )

    // 最后一个 observer 离场 == 用户离开这一页。
    unsubscribe()
    await sleep(50)

    assert.equal(
      query.state.fetchStatus,
      'idle',
      'observer 全部卸载后必须退回 idle;留在 fetching 就是永久钉死'
    )

    client.clear()
  })
})
