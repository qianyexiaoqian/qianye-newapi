/*
 * 批量粘贴的兑换码怎么切：一行一条、去空白、不去重、不改大小写。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { qyMallParseCodes } from '../codes'

describe('qyMallParseCodes', () => {
  test('按换行切，Windows 换行也认，首尾空白与空行丢掉', () => {
    assert.deepEqual(qyMallParseCodes('  A-1 \r\n\nB-2\n   \nC-3'), [
      'A-1',
      'B-2',
      'C-3',
    ])
  })

  test('不去重、不改大小写：重复与大小写差异由后端逐条判', () => {
    assert.deepEqual(qyMallParseCodes('abc\nabc\nABC'), ['abc', 'abc', 'ABC'])
  })

  test('空文本得到空数组', () => {
    assert.deepEqual(qyMallParseCodes(''), [])
    assert.deepEqual(qyMallParseCodes('\n\n'), [])
  })
})
