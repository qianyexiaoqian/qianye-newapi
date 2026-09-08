/*
配置写完之后的那一屏:等一次重启。

为什么这一屏必须存在,而不是写完直接生效 —— 三条各自独立的原因见
qianye/controller/setup.go 的文件头。简短版:扩展禁用时路由一条都没注册,
而 Gin 的路由表在服务起来之后不能再加。

这一屏只做两件事:给出重启的办法,然后等扩展真的上线。
*/
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'

import { getQySetupStatus, restartForQySetup } from './api'

const RESTART_COMMAND = 'docker compose restart new-api'

/** POLL_INTERVAL_MS 是等待扩展上线的轮询间隔。 */
const POLL_INTERVAL_MS = 2000

interface RestartPanelProps {
  configPath: string
  inContainer: boolean
  onLive: () => void
}

export function RestartPanel(props: RestartPanelProps) {
  const { t } = useTranslation()
  const [restarting, setRestarting] = useState(false)
  const [waited, setWaited] = useState(0)

  // 轮询到扩展上线为止。它同时覆盖两条路径:点了按钮的、以及自己去敲命令的。
  useEffect(() => {
    let cancelled = false
    const timer = setInterval(async () => {
      const status = await getQySetupStatus()
      if (cancelled) return
      setWaited((n) => n + POLL_INTERVAL_MS)
      if (status?.enabled) {
        clearInterval(timer)
        props.onLive()
      }
    }, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [props])

  const handleRestart = async () => {
    setRestarting(true)
    const result = await restartForQySetup(t('qy_setup_err_restart'))
    if (!result.ok) {
      setRestarting(false)
      toast.error(result.message)
    }
    // 成功时不复位 restarting:进程正在退出,这一屏接下来只应该显示"等待中"。
  }

  // 30 秒还没回来,多半是 restart 策略不是 always。给出手工出路。
  const stalled = restarting && waited > 30000

  return (
    <div className='space-y-4'>
      <Alert className='border-emerald-200 bg-emerald-50 dark:border-emerald-900/60 dark:bg-emerald-950/40'>
        <AlertTitle>{t('qy_setup_written_title')}</AlertTitle>
        <AlertDescription>
          <p>{t('qy_setup_written_desc')}</p>
          <p className='mt-2 font-mono text-xs break-all'>{props.configPath}</p>
          <p className='text-muted-foreground mt-2 text-xs'>
            {t('qy_setup_written_once')}
          </p>
        </AlertDescription>
      </Alert>

      {props.inContainer && (
        <div className='bg-card space-y-3 rounded-lg border p-4'>
          <Button type='button' onClick={handleRestart} disabled={restarting}>
            {restarting
              ? t('qy_setup_btn_restarting')
              : t('qy_setup_btn_restart')}
          </Button>
          <p className='text-muted-foreground text-xs'>
            {t('qy_setup_restart_policy_note')}
          </p>
        </div>
      )}

      {(!props.inContainer || stalled) && (
        <div className='bg-card space-y-2 rounded-lg border p-4'>
          <p className='text-sm font-medium'>{t('qy_setup_manual_title')}</p>
          <pre className='bg-muted overflow-x-auto rounded-md p-3 font-mono text-xs'>
            {RESTART_COMMAND}
          </pre>
          <p className='text-muted-foreground text-xs'>
            {t('qy_setup_manual_note')}
          </p>
        </div>
      )}

      <p className='text-muted-foreground text-center text-sm' role='status'>
        {t('qy_setup_waiting')}
      </p>
    </div>
  )
}
