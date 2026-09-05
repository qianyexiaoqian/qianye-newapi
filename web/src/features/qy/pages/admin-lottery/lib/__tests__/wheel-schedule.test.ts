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
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import {
  qyLotDraftToInput,
  qyLotEmptyDraft,
  qyLotValidateDraft,
} from '../draft'

const here = dirname(fileURLToPath(import.meta.url))
const read = (rel: string) => readFileSync(join(here, rel), 'utf8')

const TIME_ERRORS = [
  'qy_lot_v_time_order',
  'qy_lot_v_reveal_delay',
  'qy_lot_v_deadline_order',
]
const yaml = {
  reveal_delay_seconds: 60,
} as unknown as Parameters<typeof qyLotValidateDraft>[1]

describe('转盘像抽卡卡池：只有开始 / 结束两个时刻', () => {
  test('草稿只填开始与结束，开奖时刻与结算截止为 0 也不算时间错误', () => {
    const draft = {
      ...qyLotEmptyDraft(0),
      draw_mode: 'wheel' as const,
      open_at: 100,
      close_at: 200,
      draw_at: 0,
      settle_deadline: 0,
    }
    const errors = qyLotValidateDraft(draft, yaml, 0, 0)
    for (const key of TIME_ERRORS) {
      assert.ok(!errors.includes(key), `转盘不该报 ${key}：${errors.join(',')}`)
    }
    // 结束不晚于开始仍然是错：派生不等于不校验。
    const backwards = qyLotValidateDraft(
      { ...draft, close_at: 100 },
      yaml,
      0,
      0
    )
    assert.ok(backwards.includes('qy_lot_v_time_order'))
  })

  test('同样的时刻放到批次玩法上照旧要求开奖时刻', () => {
    const draft = {
      ...qyLotEmptyDraft(0),
      draw_mode: 'prob' as const,
      open_at: 100,
      close_at: 200,
      draw_at: 0,
      settle_deadline: 0,
    }
    assert.ok(
      qyLotValidateDraft(draft, yaml, 0, 0).includes('qy_lot_v_time_order')
    )
  })

  test('提交时转盘的 draw_at / settle_deadline 恒为 0，由后端按结束时间派生', () => {
    const draft = {
      ...qyLotEmptyDraft(0),
      draw_mode: 'wheel' as const,
      open_at: 100,
      close_at: 200,
      draw_at: 999,
      settle_deadline: 999,
    }
    const input = qyLotDraftToInput(draft)
    assert.equal(input.draw_at, 0)
    assert.equal(input.settle_deadline, 0)
    assert.equal(input.close_at, 200)
  })

  test('向导的时间步对转盘不渲染开奖时刻与结算截止', () => {
    const wizard = read('../../components/lottery-create-wizard.tsx')
    assert.ok(wizard.includes("t('qy_lot_wheel_end_at')"))
    assert.ok(
      wizard.includes(
        "hint={isWheel ? t('qy_lot_wheel_end_at_hint') : undefined}"
      )
    )
  })
})

describe('建活动向导不能被误触关掉', () => {
  test('向导声明 dismissible={false}，弹窗只拦 outside-press / escape-key 两种理由', () => {
    const wizard = read('../../components/lottery-create-wizard.tsx')
    assert.ok(
      wizard.includes('dismissible={false}'),
      '项目方原话：「经常性因为误触旁边空白导致窗口关闭，丢失很多编辑的信息」'
    )
    const dialog = read('../../../../components/qy-responsive-dialog.tsx')
    assert.ok(dialog.includes("details?.reason === 'outside-press'"))
    assert.ok(dialog.includes("details?.reason === 'escape-key'"))
    // × 按钮与程序性关闭必须照常：拦截条件里不能出现 close-press。
    assert.ok(!dialog.includes("'close-press'"))
  })
})

describe('建活动向导可以勾「创建后立即发布」', () => {
  test('勾了就在创建成功后接着调发布接口，失败不吞', () => {
    const wizard = read('../../components/lottery-create-wizard.tsx')
    assert.ok(wizard.includes("t('qy_lot_publish_now')"))
    assert.ok(wizard.includes('await publishQyLotActivity(data.act_no)'))
    assert.ok(wizard.includes("t('qy_lot_publish_now_failed'"))
  })
})
