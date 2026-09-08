/*
引导向导第 5 屏 —— 配置千夜扩展的独立数据库。

# 它为什么排在系统初始化之后,而不是和前四步并列

前四步跑在**匿名**窗口里(上游 /api/setup 就是匿名的)。而这一步要写的是
数据库凭据,还要 ping 一个人指定的 host:port —— 那不该出现在一个还没有
管理员的窗口上。所以流程是:先完成初始化把 root 建出来,再用刚填的账密
自动登录,拿到超管身份之后才进这一屏。后端四条端点里的三条都要 RootAuth。

# 主库为什么不在这里配

配不了:向导页本身要读主库才渲染得出来(初始化状态从库里读、root 用户
往库里写)。主库只能来自 SQL_DSN 环境变量。这一屏配的是扩展库 ——
它有一条主库没有的性质:配置文件缺失 = 扩展静默禁用,主程序照常运行。
*/
import { zodResolver } from '@hookform/resolvers/zod'
import { Database, Loader2, ShieldCheck } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { login } from '@/features/auth/api'
import { useStatus } from '@/hooks/use-status'
import { applyAuthBundle, isAuthBundle } from '@/lib/auth-session'
import { useAuthStore } from '@/stores/auth-store'

import { applyQyDatabase, testQyDatabase, type QyProbeReport } from './api'
import { ConnectionFields, ProbeSummary } from './connection-fields'
import {
  QY_DATABASE_DEFAULTS,
  qyDatabaseSchema,
  type QyDatabaseFormValues,
} from './lib/schema'
import { RestartPanel } from './restart-panel'

interface QyDatabaseStepProps {
  /** 刚在向导里创建的管理员账号,用于自动登录换取超管身份。 */
  adminUsername: string
  adminPassword: string
  /** 跳过或全部完成时调用,由向导决定去哪。 */
  onFinish: () => void
}

type Phase = 'form' | 'restart'

/** 见 alreadySignedIn 处的说明:必须活过组件重挂载,所以放在模块级。 */
let signInStarted = false

export function QyDatabaseStep(props: QyDatabaseStepProps) {
  const { t } = useTranslation()
  const { status } = useStatus()
  const [phase, setPhase] = useState<Phase>('form')
  const [authed, setAuthed] = useState(false)
  const [busy, setBusy] = useState<'test' | 'apply' | null>(null)
  const [probe, setProbe] = useState<QyProbeReport | null>(null)
  const [serverError, setServerError] = useState('')
  const [applied, setApplied] = useState<{
    configPath: string
    inContainer: boolean
  } | null>(null)

  const form = useForm<QyDatabaseFormValues>({
    resolver: zodResolver(qyDatabaseSchema),
    defaultValues: QY_DATABASE_DEFAULTS,
    mode: 'onBlur',
  })

  const encryptionEnabled =
    (status?.password_login_encryption_enabled ??
      status?.data?.password_login_encryption_enabled ??
      false) === true

  // alreadySignedIn / signInStarted 一起保证「自动登录最多发生一次」。
  //
  // 这是防死循环,不是优化:每次登录都开一个新会话(新 SID),applyAuthBundle
  // 写进 auth store 之后这一屏会被**整个重挂载** —— 而重挂载把 useRef 一起
  // 重置,于是"我记得登过"这种守卫失效、再登一次,如此往复,直到撞上后端登录
  // 限流的 429(实测:无守卫 151 次请求,useRef 版仍有 4 次)。
  //
  // 所以判据分两层,缺一不可:
  //   alreadySignedIn —— 语义正确的那一条:已经有会话就什么都不用做;
  //   signInStarted   —— 模块级变量,盖住"第一次登录还在飞"的那个窗口。
  //                      它必须在组件外面,正是因为要活过重挂载。
  const alreadySignedIn = Boolean(useAuthStore.getState().auth.accessToken)

  useEffect(() => {
    // 已经有会话就什么都不用做 —— 这才是语义正确的判据,而不是"我记得登过"。
    if (alreadySignedIn) {
      setAuthed(true)
      return
    }
    if (signInStarted) return
    signInStarted = true

    // 登录失败不阻断:这一屏本来就可以跳过,把人卡在一个"登录失败"的死屏上,
    // 比让他之后手工编辑 YAML 更糟。
    //
    // 刻意不用登录页的 handleLoginSuccess:它落地会话之后会 navigate 去
    // /dashboard,那会把人从向导里带走。
    //
    // 也刻意**没有** cancelled 标志。那个常见写法在这里会与上面的一次性守卫
    // 打架:StrictMode 下 React 先跑 effect、清理、再跑一次;清理把第一次标记成
    // cancelled,而第二次被守卫挡回去 —— 结果是登录请求真的发出去并成功了,
    // 返回的会话却被丢掉,界面永远停在"正在登录"(实测如此)。
    // React 18 起对卸载后 setState 不再告警,所以不需要那道保护。
    const run = async () => {
      try {
        const res = await login({
          username: props.adminUsername,
          password: props.adminPassword,
          passwordEncryptionEnabled: encryptionEnabled,
        })
        if (!res?.success) {
          setAuthed(false)
          return
        }
        // 会话本身靠后端下发的 cookie(api 实例是 withCredentials)成立,
        // 所以 success 就足以判定"已登录"。applyAuthBundle 是额外的一步:
        // 把 access token 写进 auth store,让后续请求也带上 Authorization 头。
        // 少了它,实测点「测试连接」会回 "Unauthorized, invalid access token"。
        if (isAuthBundle(res.data)) {
          applyAuthBundle(res.data)
        }
        setAuthed(true)
      } catch {
        setAuthed(false)
      }
    }
    run()
  }, [
    props.adminUsername,
    props.adminPassword,
    encryptionEnabled,
    alreadySignedIn,
  ])

  const handleTest = async () => {
    const valid = await form.trigger()
    if (!valid) return
    setBusy('test')
    setServerError('')
    setProbe(null)
    const result = await testQyDatabase(
      form.getValues(),
      t('qy_setup_err_unreachable')
    )
    setBusy(null)
    if (!result.ok) {
      setServerError(result.message)
      return
    }
    setProbe(result.data)
  }

  const handleApply = async () => {
    const valid = await form.trigger()
    if (!valid) return
    setBusy('apply')
    setServerError('')
    const result = await applyQyDatabase(
      form.getValues(),
      t('qy_setup_err_write')
    )
    setBusy(null)
    if (!result.ok) {
      setServerError(result.message)
      return
    }
    setApplied({
      configPath: result.data.config_path,
      inContainer: result.data.in_container,
    })
    setPhase('restart')
  }

  if (phase === 'restart' && applied) {
    return (
      <RestartPanel
        configPath={applied.configPath}
        inContainer={applied.inContainer}
        onLive={props.onFinish}
      />
    )
  }

  return (
    <div className='space-y-4'>
      <Alert>
        <AlertTitle className='flex items-center gap-2'>
          <Database className='size-4' aria-hidden='true' />
          {t('qy_setup_title')}
        </AlertTitle>
        <AlertDescription>{t('qy_setup_desc')}</AlertDescription>
      </Alert>

      {!authed && (
        <Alert className='border-amber-200 bg-amber-50 dark:border-amber-900/60 dark:bg-amber-950/40'>
          <AlertDescription className='flex items-start gap-2'>
            <ShieldCheck className='mt-0.5 size-4 text-amber-500' />
            {t('qy_setup_signing_in')}
          </AlertDescription>
        </Alert>
      )}

      <ConnectionFields form={form} />

      {serverError && (
        <Alert
          role='alert'
          className='border-destructive/40 bg-destructive/5 text-destructive'
        >
          <AlertTitle>{t('qy_setup_probe_failed')}</AlertTitle>
          <AlertDescription className='whitespace-pre-wrap'>
            {serverError}
          </AlertDescription>
        </Alert>
      )}

      {probe && <ProbeSummary report={probe} />}

      <div className='flex flex-wrap gap-2'>
        <Button
          type='button'
          variant='outline'
          onClick={handleTest}
          disabled={busy !== null}
        >
          {busy === 'test' && (
            <Loader2 className='mr-2 size-4 animate-spin' aria-hidden='true' />
          )}
          {t('qy_setup_btn_test')}
        </Button>
        <Button type='button' onClick={handleApply} disabled={busy !== null}>
          {busy === 'apply' && (
            <Loader2 className='mr-2 size-4 animate-spin' aria-hidden='true' />
          )}
          {t('qy_setup_btn_save')}
        </Button>
        <Button type='button' variant='ghost' onClick={props.onFinish}>
          {t('qy_setup_btn_skip')}
        </Button>
      </div>
    </div>
  )
}
