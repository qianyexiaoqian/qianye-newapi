/*
 * 管理员在一张订单上能做什么。判据与后端 `mall/order.go` 的状态机同源。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { qyMallAdminActions } from '../order-actions'

describe('qyMallAdminActions', () => {
  test('实物待发货：发货 / 标记失败 / 查看地址', () => {
    assert.deepEqual(qyMallAdminActions({ kind: 'physical', status: 'paid' }), [
      'ship',
      'fail',
      'reveal_address',
    ])
  })

  test('实物已发货:不能再发货,可标记完成或标记失败', () => {
    assert.deepEqual(
      qyMallAdminActions({ kind: 'physical', status: 'shipped' }),
      ['complete', 'fail', 'reveal_address']
    )
  })

  test('实物终态只剩查看地址（过没过保留期由后端回答）', () => {
    for (const status of ['done', 'cancelled', 'failed']) {
      assert.deepEqual(qyMallAdminActions({ kind: 'physical', status }), [
        'reveal_address',
      ])
    }
  })

  test('兑换码只有 done 能撤回，撤回过的不能再撤', () => {
    assert.deepEqual(qyMallAdminActions({ kind: 'code', status: 'done' }), [
      'revoke_code',
    ])
    assert.deepEqual(
      qyMallAdminActions({ kind: 'code', status: 'revoked' }),
      []
    )
  })

  test('套餐只有 held 需要裁决；paid / done / failed 没有任何人工动作', () => {
    assert.deepEqual(qyMallAdminActions({ kind: 'plan', status: 'held' }), [
      'adjudicate',
    ])
    for (const status of ['paid', 'done', 'failed']) {
      assert.deepEqual(qyMallAdminActions({ kind: 'plan', status }), [])
    }
  })
})
