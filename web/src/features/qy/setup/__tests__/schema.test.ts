import { describe, expect, it } from 'vitest'

import { QY_DATABASE_DEFAULTS, qyDatabaseSchema } from '../lib/schema'

/*
这份校验的分界线是实测出来的,不是猜的(见 qianye/config/bootstrap_file.go):
go-sql-driver 能正确处理**密码**里的 / 与 @,但主机 / 用户名 / 库名里的
分隔字符会真的串场。两半都必须钉住 —— 只钉后一半的话,某天有人"顺手"
把密码也一起拦掉,而那会让一批合法密码无法在向导里配置。
*/

function values(overrides: Partial<Record<string, string>> = {}) {
  return { ...QY_DATABASE_DEFAULTS, user: 'qy_user', ...overrides }
}

describe('qyDatabaseSchema', () => {
  it('accepts a complete set of connection parameters', () => {
    expect(qyDatabaseSchema.safeParse(values()).success).toBe(true)
  })

  it('accepts a password containing / and @ because the driver handles them', () => {
    const result = qyDatabaseSchema.safeParse(values({ password: 'p@ss/word' }))
    expect(result.success).toBe(true)
  })

  it('accepts an empty password', () => {
    expect(qyDatabaseSchema.safeParse(values({ password: '' })).success).toBe(
      true
    )
  })

  it.each([
    ['host', 'evil.com:1)/db'],
    ['user', 'u:x@y'],
    ['database', 'qianye?charset=latin1'],
  ])('rejects DSN separators in %s', (field, value) => {
    const result = qyDatabaseSchema.safeParse(values({ [field]: value }))
    expect(result.success).toBe(false)
  })

  it.each([
    ['host', ''],
    ['user', '   '],
    ['database', ''],
    ['port', ''],
  ])('requires %s to be filled in', (field, value) => {
    const result = qyDatabaseSchema.safeParse(values({ [field]: value }))
    expect(result.success).toBe(false)
  })

  it.each(['3306/x', '0', '70000', 'abc', '3306.5'])(
    'rejects %s as a port',
    (port) => {
      expect(qyDatabaseSchema.safeParse(values({ port })).success).toBe(false)
    }
  )

  it('reports a translation key rather than an English sentence', () => {
    const result = qyDatabaseSchema.safeParse(values({ host: '' }))
    expect(result.success).toBe(false)
    if (result.success) return
    // FormMessage 对 message 做 t(),所以这里必须是键而不是成品文案 ——
    // 写成英文句子的话中文界面上会直接漏出英文。
    expect(result.error.issues[0]?.message).toMatch(/^qy_setup_err_/)
  })
})
