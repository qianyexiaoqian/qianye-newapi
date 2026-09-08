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
 * 「被内容审核拦下的请求，要能在使用记录页读懂」。
 *
 * 项目方原话：「使用记录处，被阻断的 AI 审核（前置审核）应当显示在这里，
 * 让用户查阅」。后端为这一档补了一行 type=5 的日志（qianye/modules/violation/
 * usagelog.go）；这里守的是前端那一半 —— 它必须被认出来、说清楚，而且
 * **不能**被误当成一条违规扣费日志。
 *
 * 最后那一条是这个文件真正的价值：拦截日志与扣费日志共用一套 `violation_*`
 * 前缀，一旦判据写串，详情弹窗会弹出一个「违规扣费 · 费用 0」的板块 ——
 * 一句比不显示更糟的话。
 *
 * 与 reject-reason.test.tsx 同一套 harness（真组件 + 真 DetailsDialog）：
 * i18n 在 test-setup 里以空词典初始化，所以 `t(key)` 原样返回键名，
 * 断言因此落在键上。键与两份语言包的对齐由
 * features/qy/lib/__tests__/i18n-key-coverage.test.ts 单独把关。
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

import type { UsageLog } from '../../data/schema'
import type { LogOtherData } from '../../types'
import { DetailsDialog } from '../dialogs/details-dialog'

const queryClients: QueryClient[] = []

function makeLog(type: number, other: LogOtherData): UsageLog {
  return {
    id: 1,
    user_id: 1,
    created_at: 1,
    type,
    content: '请求被内容审核拦截(破限):内容涉及越狱指令',
    username: 'user',
    token_name: 'tk-live',
    model_name: 'gpt-test',
    quota: 0,
    prompt_tokens: 0,
    completion_tokens: 0,
    use_time: 0,
    is_stream: false,
    channel: 0,
    channel_name: '',
    token_id: 1,
    group: 'default',
    ip: '',
    other: JSON.stringify(other),
    request_id: 'req-block-1',
    upstream_request_id: '',
  }
}

/** 后端 blockedUsageLogRow 写出来的那一份 other（普通用户视角：已剥掉 admin_info）。 */
const BLOCKED_OTHER: LogOtherData = {
  violation_blocked: true,
  violation_code: 'qy_violation',
  qy_violation_rec_no: 'vr_req-block-1_71',
  qy_reason: '内容涉及越狱指令',
  qy_violation_category: '破限',
}

function renderDetails(log: UsageLog, isAdmin: boolean): void {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const freshAt = Date.now() + 60_000
  queryClient.setQueryData(['status'], {}, { updatedAt: freshAt })
  queryClients.push(queryClient)

  render(
    <QueryClientProvider client={queryClient}>
      <DetailsDialog
        log={log}
        isAdmin={isAdmin}
        isRoot={false}
        open
        onOpenChange={() => undefined}
      />
    </QueryClientProvider>
  )
}

afterEach(() => {
  for (const queryClient of queryClients) {
    queryClient.clear()
  }
  queryClients.length = 0
})

describe('usage log content-review block', () => {
  test('普通用户能读到拦截原因与违规类型', () => {
    // 这一页是用户遇到「请求失败了」时第一个打开的地方。原因读不到，
    // 他唯一的下一步就是发工单。
    renderDetails(makeLog(5, BLOCKED_OTHER), false)

    expect(screen.getByText('qy_log_violation_blocked')).toBeInTheDocument()
    expect(screen.getByText('破限')).toBeInTheDocument()
    expect(screen.getByText('内容涉及越狱指令')).toBeInTheDocument()
    expect(screen.getByText('vr_req-block-1_71')).toBeInTheDocument()
  })

  test('没扣到费的拦截不弹「违规扣费」板块', () => {
    // 拦截日志刻意不带 violation_fee_code，正是为了不触发这个板块：
    // 它会写出一行「费用 0」，把一次没花钱的拦截说成一次扣费。
    renderDetails(makeLog(5, BLOCKED_OTHER), true)

    expect(screen.queryByText('Violation Fee')).toBeNull()
    expect(screen.queryByText('Fee Amount')).toBeNull()
  })

  test('拦了也罚的那一次，两个板块都在', () => {
    // 扣到费的路径走的是消费日志（type=2），它同时带 violation_fee 与
    // violation_blocked。只显示扣费板块的话，用户会以为请求成功了、
    // 钱花在了模型上。
    renderDetails(
      makeLog(2, {
        ...BLOCKED_OTHER,
        violation_fee: true,
        violation_fee_code: 'qy_violation',
        fee_quota: 1500,
      }),
      false
    )

    expect(screen.getByText('qy_log_violation_blocked')).toBeInTheDocument()
    expect(screen.getByText('Violation Fee')).toBeInTheDocument()
  })

  test('会话被安全策略屏蔽时换一句抬头，而不是说成内容审核', () => {
    // 两种拦截给用户的下一步完全相反：内容审核是"改内容"，cyber 会话屏蔽是
    // "开一条新会话" —— 后者改多少遍内容都没有用。共用一句抬头等于把人按在
    // 一个不会有结果的循环里。
    renderDetails(
      makeLog(5, {
        violation_blocked: true,
        violation_block_kind: 'session',
        violation_code: 'session_blocked_by_cyber_policy',
        qy_reason: '此会话已被安全策略屏蔽,请开启新会话',
      }),
      false
    )

    expect(screen.getByText('qy_log_session_blocked')).toBeInTheDocument()
    expect(screen.queryByText('qy_log_violation_blocked')).toBeNull()
    expect(
      screen.getByText('此会话已被安全策略屏蔽,请开启新会话')
    ).toBeInTheDocument()
  })

  test('老日志没有 block_kind 时按内容审核处理', () => {
    // `violation_block_kind` 上线前写下的那批行只有内容审核一种，缺省不能
    // 让它们变成一片空白或一句会话屏蔽。
    renderDetails(makeLog(5, BLOCKED_OTHER), false)
    expect(screen.getByText('qy_log_violation_blocked')).toBeInTheDocument()
  })

  test('普通的错误日志不会误报成拦截', () => {
    // 判据必须是 violation_blocked 这个显式标记，不是「type=5」。
    // 写成后者的话，每一条上游错误日志都会挂上一块「内容审核拦截」。
    renderDetails(makeLog(5, { request_path: '/v1/chat/completions' }), false)

    expect(screen.queryByText('qy_log_violation_blocked')).toBeNull()
  })
})
