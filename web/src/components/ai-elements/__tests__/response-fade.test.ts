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
// 同步自上游 137d1171f 的 response-fade 单测。上游那份写的是 vitest
// (`vi.spyOn(performance,'now')` + `toMatchObject`),本仓的前端闸门跑的是
// node:test + node:assert(见 web/scripts/run-tests.mjs 顶部那段说明),
// 所以逐条改写成同一批判据的 node:test 版本:时钟用可写的 performance.now
// 桩,断言逐字段展开。判据本身一条不减。
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import {
  beginRun,
  classifyValue,
  createFadeState,
  endRun,
  FADE_DURATION_MS,
  FADE_HYDRATION_THRESHOLD,
  FADE_STAGGER_MAX_MS,
  FADE_STAGGER_MS,
  splitWords,
  stageRun,
} from '../response-fade'

const realNow = performance.now.bind(performance)
let fakeNow = 1000

// 淡入是「一次性」的:是否还在动画窗口内完全由 performance.now 与上一轮的
// 起始时刻之差决定。真时钟下这些用例会随机器快慢漂移,必须钉死。
performance.now = () => fakeNow

after(() => {
  performance.now = realNow
})

function setNow(value: number) {
  fakeNow = value
}

describe('splitWords', () => {
  test('round-trips ASCII words with trailing whitespace', () => {
    const value = 'Hello world,  stream.\n'
    assert.equal(splitWords(value).join(''), value)
  })

  test('keeps leading whitespace as its own part', () => {
    assert.deepEqual(splitWords('  hi'), ['  ', 'hi'])
  })

  test('segments CJK without spaces via Intl.Segmenter', () => {
    const value = '你好世界'
    const parts = splitWords(value)
    assert.equal(parts.join(''), value)
    assert.ok(parts.length > 1)
  })
})

describe('classifyValue', () => {
  test('animates only newly appended words with capped stagger', () => {
    setNow(1000)
    const state = createFadeState()
    const first = beginRun(state)
    const firstSegments = classifyValue(first, 'one two ')
    endRun(first)

    assert.equal(firstSegments.filter((s) => s.animated).length, 2)
    assert.equal(firstSegments[0]?.delay, 0)
    assert.equal(firstSegments[1]?.delay, FADE_STAGGER_MS)

    setNow(1000 + FADE_DURATION_MS + FADE_STAGGER_MS + 1)
    const second = beginRun(state)
    const secondSegments = classifyValue(second, 'one two three four')
    endRun(second)

    const animated = secondSegments.filter((s) => s.animated)
    assert.deepEqual(
      animated.map((s) => s.value.trim()),
      ['three', 'four']
    )
    assert.equal(animated[0]?.delay, 0)
    assert.equal(animated[1]?.delay, FADE_STAGGER_MS)
  })

  test('caps stagger delay at FADE_STAGGER_MAX_MS', () => {
    setNow(1000)
    const state = createFadeState()
    const run = beginRun(state)
    const words = Array.from({ length: 20 }, (_, i) => `w${i}`).join(' ')
    const segments = classifyValue(run, words)
    endRun(run)

    const delays = segments.filter((s) => s.animated).map((s) => s.delay)
    assert.equal(Math.max(...delays), FADE_STAGGER_MAX_MS)
  })

  test('replays identical delay while still inside the animation window', () => {
    setNow(1000)
    const state = createFadeState()
    const first = beginRun(state)
    classifyValue(first, 'hello ')
    endRun(first)

    setNow(1000 + FADE_DURATION_MS / 2)
    const second = beginRun(state)
    const segments = classifyValue(second, 'hello world')
    endRun(second)

    assert.equal(segments[0]?.animated, true)
    assert.equal(segments[0]?.delay, 0)
    assert.equal(segments[0]?.start, 0)
    assert.equal(segments[0]?.value, 'hello ')
    assert.equal(segments[1]?.start, 6)
    assert.equal(segments[1]?.value, 'world')
  })

  test('keeps the same start offset when the head word grows', () => {
    setNow(1000)
    const state = createFadeState()
    const first = beginRun(state)
    const head = classifyValue(first, 'hel')
    endRun(first)
    assert.equal(head[0]?.start, 0)

    setNow(1050)
    const second = beginRun(state)
    const grown = classifyValue(second, 'hello')
    endRun(second)

    assert.equal(grown[0]?.start, 0)
    assert.equal(grown[0]?.animated, true)
    assert.equal(grown[0]?.value, 'hello')
  })

  test('does not animate whitespace-only parts', () => {
    setNow(1000)
    const state = createFadeState()
    const run = beginRun(state)
    const segments = classifyValue(run, '  \n')
    endRun(run)

    assert.ok(segments.every((s) => !s.animated))
  })

  test('suppresses animation on the hydration baseline', () => {
    setNow(1000)
    const state = createFadeState()
    const longText = 'a'.repeat(FADE_HYDRATION_THRESHOLD + 1)
    const run = beginRun(state, true)
    const segments = classifyValue(run, longText)
    endRun(run)

    assert.ok(segments.every((s) => !s.animated))
    assert.equal(state.prevCount, longText.length)
  })

  test('stops replaying animation after the window expires', () => {
    setNow(1000)
    const state = createFadeState()
    const first = beginRun(state)
    classifyValue(first, 'done ')
    endRun(first)

    setNow(1000 + FADE_DURATION_MS + 1)
    const second = beginRun(state)
    const segments = classifyValue(second, 'done next')
    endRun(second)

    assert.equal(segments[0]?.animated, false)
    assert.equal(segments[0]?.value, 'done ')
    assert.equal(segments[1]?.animated, true)
    assert.equal(segments[1]?.value, 'next')
    assert.equal(state.active.has(0), false)
  })

  test('abandoned staged runs leave committed state untouched', () => {
    setNow(1000)
    const state = createFadeState()
    const first = beginRun(state)
    classifyValue(first, 'keep ')
    endRun(first)
    assert.equal(state.prevCount, 5)

    const abandoned = beginRun(state)
    classifyValue(abandoned, 'keep extra')
    stageRun(abandoned)
    // Never commit — simulate React discarding the render
    state.pending = null

    assert.equal(state.prevCount, 5)
    assert.equal(state.active.size, 1)
  })
})
