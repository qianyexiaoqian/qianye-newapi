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
import { readFileSync } from 'node:fs'
import { afterEach, describe, test } from 'node:test'

import { api } from '@/lib/api'

import { qyKeys } from '../../../lib/query-keys'
import { listQyAuditLogs, listQyPiiAudits, listQyRequestAudits } from '../api'
import { qyAuditTrimmed } from '../shared'

/**
 * 审计中心的接线。
 *
 * 这一页要防的缺陷全是**断链**形状:
 *
 *  - `qy_pii_audits` 的接口在、query key 在,却没有任何页面消费它 ——
 *    向用户承诺「谁看过你的银行卡都有记录」,而平台自己调不出这份记录。
 *  - 请求台账是新表,前端漏了任何一环(路由写错、参数名写错)都不会报错,
 *    只会让那个 tab 永远是空的,而空列表看起来与「这段时间没有请求」一样。
 *
 * 因此断言的是**请求真的发到了那个后端路由、并且带着后端认得的参数名**,
 * 而不是组件长什么样。做法是换掉 axios 适配器、跑真实的 `qyGet` 代码路径。
 */

type Recorded = { method: string; url: string; params: Record<string, unknown> }

const realAdapter = api.defaults.adapter

function captureRequest(responseData: unknown): Recorded[] {
  const calls: Recorded[] = []
  api.defaults.adapter = async (config) => {
    calls.push({
      method: (config.method ?? '').toLowerCase(),
      url: config.url ?? '',
      params: (config.params ?? {}) as Record<string, unknown>,
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

const emptyPage = { items: [], total: 0, p: 1, page_size: 20 }

afterEach(() => {
  api.defaults.adapter = realAdapter
})

describe('audit centre API wiring', () => {
  test('fund audit sends every filter the backend handler reads', async () => {
    const calls = captureRequest(emptyPage)

    await listQyAuditLogs({
      p: 2,
      page_size: 20,
      category: 'withdraw',
      action: 'withdraw.',
      result: 'fail',
      actor_type: 'admin',
      ip: '10.0.0.1',
      trace_no: 'WD1',
      actor_user_id: 7,
      target_user_id: 9,
      start_ts: 1_700_000_000,
      end_ts: 1_800_000_000,
    })

    assert.equal(calls.length, 1)
    assert.equal(calls[0].method, 'get')
    assert.equal(calls[0].url, '/api/qy/admin/audit-logs')
    // 参数名逐个钉死:后端读的是这些名字,前端拼错一个字母不会报错,
    // 只会让那个筛选静默失效 —— 而失效的筛选返回的是「全部」,
    // 看起来完全正常。end_ts 曾经是后端支持、前端根本没声明的那一类。
    assert.deepEqual(calls[0].params, {
      p: 2,
      page_size: 20,
      category: 'withdraw',
      action: 'withdraw.',
      result: 'fail',
      actor_type: 'admin',
      ip: '10.0.0.1',
      trace_no: 'WD1',
      actor_user_id: 7,
      target_user_id: 9,
      start_ts: 1_700_000_000,
      end_ts: 1_800_000_000,
    })
  })

  test('request log hits /admin/request-audits with the tri-state success flag', async () => {
    const calls = captureRequest(emptyPage)

    await listQyRequestAudits({
      p: 1,
      page_size: 20,
      action: 'admin.withdraw.',
      method: 'POST',
      // 字符串而不是布尔:后端读的是 c.Query("success"),
      // 布尔 false 经 axios 序列化后同样是 "false",但显式用字符串
      // 才能保证「不传 = 全部」这个三态不会被某次重构压成两态。
      success: 'false',
      ip: '1.2.3.4',
      request_id: 'req-1',
      actor_user_id: 9,
      target_user_id: 4,
      start_ts: 1_700_000_000,
    })

    assert.equal(calls.length, 1)
    assert.equal(calls[0].url, '/api/qy/admin/request-audits')
    assert.equal(calls[0].params.success, 'false')
    assert.equal(calls[0].params.action, 'admin.withdraw.')
    assert.equal(calls[0].params.request_id, 'req-1')
  })

  test('PII access log finally has a consumer, and it hits the existing route', async () => {
    const calls = captureRequest(emptyPage)

    await listQyPiiAudits({
      p: 1,
      page_size: 20,
      admin_id: 3,
      target_user_id: 8,
    })

    assert.equal(calls.length, 1)
    // 路由挂在 withdraw 模块下,不是 /admin/pii-audits —— 写错这一行
    // 就会 404,而页面只会显示「暂无明文访问记录」。
    assert.equal(calls[0].url, '/api/qy/admin/withdraw/pii-audits')
    assert.deepEqual(calls[0].params, {
      p: 1,
      page_size: 20,
      admin_id: 3,
      target_user_id: 8,
    })
  })

  test('空筛选不发给后端:空串会被当成「等于空字符串」而不是「不筛选」', () => {
    assert.equal(qyAuditTrimmed(''), undefined)
    assert.equal(qyAuditTrimmed('   '), undefined)
    assert.equal(qyAuditTrimmed(' withdraw. '), 'withdraw.')
  })

  test('两张台账的 query key 都在 qy 前缀下,资金操作能整片失效它们', () => {
    assert.equal(qyKeys.adminAuditLogs({})[0], 'qy')
    assert.equal(qyKeys.adminRequestAudits({})[0], 'qy')
    assert.equal(qyKeys.adminWithdrawPiiAudits({})[0], 'qy')
    // 两张表必须是不同的缓存条目。共用一个 key 时,切 tab 会拿到上一张表的
    // 数据并按本表的列渲染 —— 满屏 undefined,而不是一个看得出来的错误。
    assert.notDeepEqual(
      qyKeys.adminAuditLogs({}),
      qyKeys.adminRequestAudits({})
    )
  })
})

describe('audit centre page wiring', () => {
  const page = readFileSync(new URL('../index.tsx', import.meta.url), 'utf8')

  test('三个 tab 都被页面挂上了', () => {
    // 组件写好了却没被 index 引用,是本仓反复出现的断链形状之一:
    // 文件在、导出在、typecheck 全过,而用户永远看不到那个 tab。
    for (const tab of [
      'QyFundAuditTab',
      'QyRequestAuditTab',
      'QyPiiAuditTab',
    ]) {
      assert.ok(page.includes(`<${tab} />`), `${tab} 没有被 index.tsx 渲染`)
    }
  })
})
