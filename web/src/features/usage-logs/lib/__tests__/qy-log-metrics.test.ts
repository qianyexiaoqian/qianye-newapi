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
import { describe, test } from 'node:test'

import type { UsageLog } from '../../data/schema'
import type { LogOtherData } from '../../types'
import {
  formatCacheRate,
  getCacheRate,
  getReasoning,
  normalizeEffortLevel,
} from '../qy-log-metrics'

/**
 * 最小可用的消费日志。
 *
 * `other` 留空字符串：所有用例都直接把解析后的 `LogOtherData` 传给被测函数，
 * 这样断言的是计算逻辑本身，而不是 JSON 解析。
 */
function makeLog(overrides: Partial<UsageLog> = {}): UsageLog {
  return {
    id: 1,
    user_id: 1,
    created_at: 0,
    type: 2,
    content: '',
    username: '',
    token_name: '',
    model_name: 'gpt-4o',
    quota: 0,
    prompt_tokens: 0,
    completion_tokens: 0,
    use_time: 0,
    is_stream: false,
    channel: 0,
    channel_name: '',
    token_id: 0,
    group: '',
    ip: '',
    other: '',
    request_id: '',
    upstream_request_id: '',
    ...overrides,
  }
}

describe('cache hit rate denominator selection', () => {
  test('uses the backend-fixed denominator when qy_input_total is present', () => {
    const other: LogOtherData = {
      qy_ver: 1,
      qy_semantic: 'anthropic',
      qy_input_total: 8000,
      qy_cache_read: 2000,
      qy_cache_write: 1000,
    }
    const rate = getCacheRate(
      makeLog({ prompt_tokens: 5000, completion_tokens: 100 }),
      other
    )
    assert.deepEqual(rate, {
      pct: 25,
      cacheRead: 2000,
      inputTotal: 8000,
      anomaly: false,
    })
  })

  test('reports 0% without a semantic marker when nothing was cached', () => {
    const rate = getCacheRate(
      makeLog({ prompt_tokens: 1200, completion_tokens: 30 }),
      { model_ratio: 1 }
    )
    assert.equal(rate?.pct, 0)
    assert.equal(rate?.inputTotal, 1200)
  })

  test('adds cache tokens back into the denominator for Anthropic semantics', () => {
    const rate = getCacheRate(
      makeLog({ prompt_tokens: 500, completion_tokens: 10 }),
      {
        usage_semantic: 'anthropic',
        cache_tokens: 1000,
        cache_creation_tokens: 500,
      }
    )
    assert.equal(rate?.inputTotal, 2000)
    assert.equal(rate?.pct, 50)
  })

  test('counts split 5m/1h cache writes once instead of double counting', () => {
    const rate = getCacheRate(
      makeLog({ prompt_tokens: 400, completion_tokens: 10 }),
      {
        claude: true,
        cache_tokens: 600,
        // Claude writes both the total and the 5m/1h split; summing all three
        // would inflate the denominator to 2600.
        cache_creation_tokens: 1000,
        cache_creation_tokens_5m: 600,
        cache_creation_tokens_1h: 400,
      }
    )
    assert.equal(rate?.inputTotal, 2000)
  })

  test('falls back to input_tokens_total when only that denominator is trusted', () => {
    const rate = getCacheRate(
      makeLog({ prompt_tokens: 0, completion_tokens: 50 }),
      { cache_tokens: 250, input_tokens_total: 1000 }
    )
    assert.equal(rate?.pct, 25)
  })

  test('returns null for a cached legacy log with no semantic marker', () => {
    // 这是最重要的一条：分母二义时必须渲染 `—`，
    // 猜一个公式会得到落在 0-100% 区间的静默错误值。
    assert.equal(
      getCacheRate(makeLog({ prompt_tokens: 900, completion_tokens: 20 }), {
        cache_tokens: 300,
      }),
      null
    )
  })

  test('returns null when the log carries no token usage at all', () => {
    assert.equal(getCacheRate(makeLog(), { qy_input_total: 100 }), null)
  })

  test('returns null when the other payload failed to parse', () => {
    assert.equal(
      getCacheRate(makeLog({ prompt_tokens: 100, completion_tokens: 5 }), null),
      null
    )
  })

  test('clamps to 100% and flags an anomaly when cache read exceeds the denominator', () => {
    const rate = getCacheRate(
      makeLog({ prompt_tokens: 100, completion_tokens: 5 }),
      { qy_input_total: 100, qy_cache_read: 400 }
    )
    assert.equal(rate?.pct, 100)
    assert.equal(rate?.anomaly, true)
  })

  test('propagates the backend anomaly marker', () => {
    const rate = getCacheRate(
      makeLog({ prompt_tokens: 100, completion_tokens: 5 }),
      { qy_input_total: 200, qy_cache_read: 50, qy_cache_anomaly: true }
    )
    assert.equal(rate?.anomaly, true)
  })
})

describe('cache hit rate formatting', () => {
  const cases: Array<[number, string]> = [
    [0, '0%'],
    [0.04, '<0.1%'],
    [12.34, '12.3%'],
    // 四舍五入后满格就显示 100%，而不是别扭的 "100.0%"
    [99.96, '100%'],
    [100, '100%'],
  ]

  for (const [pct, expected] of cases) {
    test(`formats ${pct} as ${expected}`, () => {
      assert.equal(formatCacheRate(pct), expected)
    })
  }
})

describe('reasoning effort normalization', () => {
  const cases: Array<[string, string]> = [
    ['none', 'none'],
    ['Minimal', 'minimal'],
    ['low', 'low'],
    ['medium', 'medium'],
    ['high', 'high'],
    ['xhigh', 'max'],
    ['auto', 'auto'],
    // 未知口径保守居中：归成 none 会让用户以为没花思考的钱。
    ['turbo-thinking', 'medium'],
  ]

  for (const [raw, level] of cases) {
    test(`maps ${raw} to ${level}`, () => {
      assert.equal(normalizeEffortLevel(raw), level)
    })
  }
})

describe('reasoning extraction', () => {
  test('prefers the normalized backend payload over the legacy string', () => {
    const reasoning = getReasoning({
      reasoning_effort: 'low',
      qy_reasoning: {
        level: 'high',
        raw: 'budget:24576',
        budget: 24576,
        src: 'claude_thinking',
      },
    })
    assert.deepEqual(reasoning, {
      level: 'high',
      raw: 'budget:24576',
      budget: 24576,
      src: 'claude_thinking',
    })
  })

  test('falls back to the legacy reasoning_effort string on older logs', () => {
    const reasoning = getReasoning({ reasoning_effort: 'high' })
    assert.equal(reasoning?.level, 'high')
    assert.equal(reasoning?.raw, 'high')
    assert.equal(reasoning?.budget, 0)
  })

  test('re-normalizes from raw when the payload level is not a known bucket', () => {
    const reasoning = getReasoning({
      qy_reasoning: { level: 'insane', raw: 'ultra', budget: 0, src: 'qwen' },
    })
    assert.equal(reasoning?.level, 'max')
  })

  test('returns null when the log has no reasoning signal', () => {
    assert.equal(getReasoning({ cache_tokens: 10 }), null)
    assert.equal(getReasoning(null), null)
  })
})
