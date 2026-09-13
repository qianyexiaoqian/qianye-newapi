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
import { AxiosError, AxiosHeaders } from 'axios'
import { describe, expect, test, vi, beforeEach, afterEach } from 'vitest'

import { handleServerError } from '../handle-server-error'

const toastError = vi.fn()
vi.mock('sonner', () => ({
  toast: { error: (...args: unknown[]) => toastError(...args) },
}))

function axiosErrorWith(data: unknown, message = 'Request failed') {
  const error = new AxiosError(message)
  error.response = {
    data,
    status: 400,
    statusText: 'Bad Request',
    headers: new AxiosHeaders(),
    config: { headers: new AxiosHeaders() },
  }
  return error
}

// 本仓后端的错误体恒为 {success:false, message:"..."},没有 title。
// 旧实现无条件取 data.title,于是每次失败都 toast.error(undefined) —— 一个
// 没有字的红条。这份用例钉的就是「永远不要弹空白」。
describe('handleServerError', () => {
  beforeEach(() => toastError.mockClear())
  afterEach(() => vi.restoreAllMocks())

  test('用后端的 message 而不是不存在的 title', () => {
    handleServerError(axiosErrorWith({ success: false, message: '余额不足' }))
    expect(toastError).toHaveBeenCalledWith('余额不足')
  })

  test('后端只给 title 时也能用', () => {
    handleServerError(axiosErrorWith({ title: '请求过于频繁' }))
    expect(toastError).toHaveBeenCalledWith('请求过于频繁')
  })

  test('两者都没有时回落到 axios 自己的 message,绝不弹空白', () => {
    handleServerError(axiosErrorWith({ success: false }, 'Network Error'))
    expect(toastError).toHaveBeenCalledWith('Network Error')
  })

  test('message 是空白字符串时同样不采用', () => {
    handleServerError(axiosErrorWith({ message: '   ' }, 'Request failed'))
    expect(toastError).toHaveBeenCalledWith('Request failed')
  })

  test('任何情况下传给 toast 的都是非空字符串', () => {
    for (const body of [null, undefined, {}, { message: null }, { title: 0 }]) {
      toastError.mockClear()
      handleServerError(axiosErrorWith(body, ''))
      const arg = toastError.mock.calls[0]?.[0]
      expect(typeof arg).toBe('string')
      expect((arg as string).trim()).not.toBe('')
    }
  })
})
