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

import {
  qyLotDraftForPlay,
  qyLotDraftToInput,
  qyLotEmptyDraft,
  qyLotPlayOf,
  type QyLotDraft,
  type QyLotPlay,
} from '../draft'

/**
 * 「三个并列的玩法」是**纯前端投影**，后端仍然是 (kind, draw_mode)（需求 6）。
 *
 * 这一层的每一条规则错了都不会有任何编译期或运行期信号，只会在提交的那一刻
 * 变成一个被后端拒绝、或者更糟 —— 被后端接受但不是运营想要的活动：
 *
 *   · 切离双色球没清 `series_no` → 后端对带期次的非双色球活动是**拒绝**，
 *     而那个字段此刻在界面上已经不可见，运营看不出请求为什么失败；
 *   · 从双色球切回抽奖没换 `draw_mode` → 表单画的是「抽奖」，提交的是双色球；
 *   · 切到竞猜没归位 `draw_mode` → 切回抽奖时留着一个上次选的 `ball`，
 *     而对应的号池表单不显示。
 */

const PLAYS: QyLotPlay[] = ['ball', 'draw', 'guess']

function draftFor(play: QyLotPlay): QyLotDraft {
  return qyLotDraftForPlay(qyLotEmptyDraft(500), play)
}

describe('qyLotPlayOf', () => {
  test('双色球的 kind 也是 draw，判定必须先看 draw_mode', () => {
    const draft = { ...qyLotEmptyDraft(500), kind: 'draw' as const }
    assert.equal(qyLotPlayOf({ ...draft, draw_mode: 'ball' }), 'ball')
    assert.equal(qyLotPlayOf({ ...draft, draw_mode: 'rank' }), 'draw')
    assert.equal(qyLotPlayOf({ ...draft, draw_mode: 'prob' }), 'draw')
  })

  test('竞猜恒为 guess，哪怕草稿里还留着一个 ball', () => {
    // 这个组合是从双色球切到竞猜之后可能出现的中间状态。判反了的话，
    // 界面会给一场竞猜画出选号表单。
    const draft = {
      ...qyLotEmptyDraft(500),
      kind: 'guess' as const,
      draw_mode: 'ball' as const,
    }
    assert.equal(qyLotPlayOf(draft), 'guess')
  })
})

describe('qyLotDraftForPlay', () => {
  test('三个玩法各自的投影都能被 qyLotPlayOf 原样读回来', () => {
    for (const play of PLAYS) {
      assert.equal(qyLotPlayOf(draftFor(play)), play)
    }
  })

  test('任意一次切换都不会留下 ball 与非双色球并存的草稿', () => {
    // 九种切换全走一遍：这是运营在第一步里真的会做的事（点一遍看看有什么）。
    for (const from of PLAYS) {
      for (const to of PLAYS) {
        const next = qyLotDraftForPlay(draftFor(from), to)
        assert.equal(qyLotPlayOf(next), to, `${from} → ${to}`)
        if (to !== 'ball') {
          assert.notEqual(next.draw_mode, 'ball', `${from} → ${to} 残留 ball`)
          assert.equal(next.series_no, '', `${from} → ${to} 残留期次`)
        }
      }
    }
  })

  test('切离双色球时期次被清掉，切回来时不会把它带回请求体', () => {
    const ball = { ...draftFor('ball'), series_no: 'S-2026-01' }
    const backToDraw = qyLotDraftForPlay(ball, 'draw')
    assert.equal(backToDraw.series_no, '')
    // 请求体是最终判据：后端对带期次的普通抽奖是拒绝而不是忽略。
    assert.equal(qyLotDraftToInput(backToDraw).series_no, '')
    assert.equal(qyLotDraftToInput(backToDraw).draw_mode, 'rank')
  })

  test('已经选好的 prob 在「抽奖」内部不被重置', () => {
    // 二级的定档方式是运营自己选的。切到竞猜再切回来会重置（那是切玩法），
    // 但重复点「抽奖」这一张卡不该把它抹掉。
    const prob = { ...draftFor('draw'), draw_mode: 'prob' as const }
    assert.equal(qyLotDraftForPlay(prob, 'draw').draw_mode, 'prob')
  })

  test('双色球保留已选期次：同一个系列反复开期是常态', () => {
    const ball = { ...draftFor('ball'), series_no: 'S-2026-01' }
    assert.equal(qyLotDraftForPlay(ball, 'ball').series_no, 'S-2026-01')
  })
})
