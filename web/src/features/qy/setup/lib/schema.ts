/*
千夜扩展库配置表单的校验 schema。

前端这一层只挡"明显填不对"的输入,让人不必等一次网络往返。真正的判据在
后端:qianye/config 的 MySQLDSN 会把参数拼成 DSN 再让驱动原样解回来,
解不回来就拒绝。两层的分工必须写清楚,否则下一个人会想把 DSN 的转义规则
抄一份到这里 —— 而那份副本必然与驱动漂移。
*/
import { z } from 'zod'

/** dsnSeparators 是会让 DSN 串场的字符。后端逐字回环校验,这里只是提前告知。 */
const dsnSeparators = /[:@/?()]/

export const qyDatabaseSchema = z.object({
  host: z
    .string()
    .trim()
    .min(1, 'qy_setup_err_host_required')
    .refine((v) => !dsnSeparators.test(v), 'qy_setup_err_host_chars'),
  port: z
    .string()
    .trim()
    .min(1, 'qy_setup_err_port_required')
    .refine((v) => {
      const n = Number(v)
      return Number.isInteger(n) && n >= 1 && n <= 65535
    }, 'qy_setup_err_port_range'),
  user: z
    .string()
    .trim()
    .min(1, 'qy_setup_err_user_required')
    .refine((v) => !dsnSeparators.test(v), 'qy_setup_err_user_chars'),
  // 密码不设字符限制:实测 go-sql-driver 能正确处理密码里的 / 与 @,
  // 在这里拦掉等于让一批合法密码无法配置。后端的回环校验兜底。
  password: z.string(),
  database: z
    .string()
    .trim()
    .min(1, 'qy_setup_err_db_required')
    .refine((v) => !dsnSeparators.test(v), 'qy_setup_err_db_chars'),
})

export type QyDatabaseFormValues = z.infer<typeof qyDatabaseSchema>

export const QY_DATABASE_DEFAULTS: QyDatabaseFormValues = {
  host: '127.0.0.1',
  port: '3306',
  user: '',
  password: '',
  database: 'qianye',
}
