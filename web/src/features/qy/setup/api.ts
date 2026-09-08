/*
千夜扩展 —— 引导部署向导里"配置扩展数据库"那一步的 API 客户端。

后端契约见 qianye/controller/setup.go。四条端点里只有 status 是匿名的,
其余三条都要超管身份 —— 因此调用它们之前必须先登录(向导用刚创建的
管理员账号自动登录)。

# 为什么走 qyGet / qyPost 而不是直接 api.post

features/qy/__tests__/qy-request-paths.ts 那条前后端对账守卫认两种形状:
qy* 包装壳,以及首参含 QY_API_PREFIX 的 api.*。而它的第三条规则会把**任何**
以 `${QY_API_PREFIX}` 开头的模板串一律记成 GET(那是给 <img src> 用的),
于是 `api.post(\`${QY_API_PREFIX}/setup/test\`)` 会被同时记成 POST 和一条
并不存在的 GET,守卫当场判 404。全仓现有的非 GET 调用都走 qy* 壳、路径不带
前缀,所以从没撞上这一条。这里跟着同一个形状走。

# 错误处理

qy* 壳在 success:false 时抛 QyError,里面带着后端的 rawMessage —— 那句话是
"请执行 ALTER DATABASE …"这一类可照做的指引,必须原样显示在表单下面。
额外传 skipAuthRefresh:401 的默认处理会把整个页面跳去 /sign-in,而这一步
正跑在向导中间,跳走等于把刚填的表单连同上下文一起丢掉。
*/
import { QyError, qyGet, qyPost } from '@/features/qy/lib/api'

export interface QySetupStatus {
  configured: boolean
  enabled: boolean
  connected: boolean
  needs_restart: boolean
  in_container: boolean
}

export interface QyDatabaseParams {
  host: string
  port: string
  user: string
  password: string
  database: string
}

export interface QyProbeReport {
  version: string
  charset: string
  table_count: number
  empty: boolean
}

export interface QyApplyResult {
  config_path: string
  in_container: boolean
}

/** QyResult 把"成功带数据"与"失败带可读原因"收敛成一个可穷举的形状。 */
export type QyResult<T> =
  | { ok: true; data: T }
  | { ok: false; message: string; code?: string }

const quiet = { skipAuthRefresh: true } as const

/**
 * toFailure 把 QyError 还原成一句人能照做的话。
 *
 * 后端把"连不上""字符集不对""没有建表权限"都返回成 400 + message,
 * 那句 message 本身就是操作指引,丢掉它换成"请求失败"等于把这一步的
 * 全部诊断价值扔掉。
 */
function toFailure(error: unknown, fallback: string): QyResult<never> {
  if (error instanceof QyError) {
    return {
      ok: false,
      message: error.rawMessage || fallback,
      code: error.code ?? undefined,
    }
  }
  return { ok: false, message: fallback }
}

/**
 * getQySetupStatus 读扩展这一步的进度。匿名可调用。
 *
 * 返回 null 表示"这个后端没有这条端点" —— 上游原版就是这样。向导据此
 * 完全隐藏扩展步骤,而不是显示一个报错的屏。
 */
export async function getQySetupStatus(): Promise<QySetupStatus | null> {
  try {
    return await qyGet<QySetupStatus>('/setup/status', { t: Date.now() }, quiet)
  } catch {
    return null
  }
}

export async function testQyDatabase(
  params: QyDatabaseParams,
  fallbackMessage: string
): Promise<QyResult<QyProbeReport>> {
  try {
    const data = await qyPost<QyProbeReport>('/setup/test', params, quiet)
    return { ok: true, data }
  } catch (error) {
    return toFailure(error, fallbackMessage)
  }
}

export async function applyQyDatabase(
  params: QyDatabaseParams,
  fallbackMessage: string
): Promise<QyResult<QyApplyResult>> {
  try {
    const data = await qyPost<QyApplyResult>('/setup/apply', params, quiet)
    return { ok: true, data }
  } catch (error) {
    return toFailure(error, fallbackMessage)
  }
}

export async function restartForQySetup(
  fallbackMessage: string
): Promise<QyResult<null>> {
  try {
    await qyPost<unknown>('/setup/restart', {}, quiet)
    return { ok: true, data: null }
  } catch (error) {
    return toFailure(error, fallbackMessage)
  }
}
