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
import { useQuery } from '@tanstack/react-query'
import { ExternalLinkIcon, RefreshCcwIcon } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Markdown } from '@/components/ui/markdown'
import { isQyError, qyErrorMessage } from '@/features/qy/lib/api'
import { qyKeys } from '@/features/qy/lib/query-keys'
import {
  checkQyUpdate,
  getQyVersion,
} from '@/features/qy/pages/admin-health/api'
import type { QyUpdateCheck } from '@/features/qy/pages/admin-health/types'
import { formatTimestamp, formatTimestampToDate } from '@/lib/format'

import { SettingsSection } from '../components/settings-section'
import { QY_UPDATE_STATUS_I18N } from './update-check-copy'

type ReleaseInfo = {
  tag_name: string
  name?: string
  body?: string
  html_url?: string
  published_at?: string
}

/**
 * 后端 `qianye/version.Unknown`：版本号未经构建期注入 / 未在 baseline.txt 声明
 * 时的取值。
 *
 * 后端刻意返回这个字面量而不是空串，就是为了让"这台机器上没有版本信息"和
 * "页面坏了"区分得开；前端必须把它翻成一句人话，否则界面上会出现一个谁也
 * 看不懂的英文单词 unknown。裸 `go build`（本地开发的常态）走的正是这一支。
 */
const QY_VERSION_UNINJECTED = 'unknown'

type UpdateCheckerSectionProps = {
  currentVersion?: string | null
  startTime?: number | null
}

export function UpdateCheckerSection({
  currentVersion,
  startTime,
}: UpdateCheckerSectionProps) {
  const { t } = useTranslation()
  const [checking, setChecking] = useState(false)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [release, setRelease] = useState<ReleaseInfo | null>(null)

  // 二开检查更新的结果**留在页面上**，而不是弹一个会消失的 toast。
  // 六种结局里有四种要求管理员读完再决定下一步（去发版 / 去查出站 IP /
  // 等一小时 / 去看 release 说明），toast 三秒就没了。
  const [forkChecking, setForkChecking] = useState(false)
  const [forkResult, setForkResult] = useState<QyUpdateCheck | null>(null)
  // 失败拆成两行：第一行是翻译过的、按 code 分档的结论（"连不上" vs "被限流"
  // vs "仓库不存在"），第二行是后端那句带根因细节的原文（具体的传输层错误、
  // 状态码、还要等几分钟）。只留前者会丢掉排障要用的细节，只留后者则永远是中文。
  const [forkError, setForkError] = useState<{
    summary: string
    detail: string | null
  } | null>(null)

  const uptime = startTime ? formatTimestamp(startTime) : t('Unknown')
  const version = currentVersion || t('Unknown')

  // 二开版本信息。复用排障页那条 `GET /api/qy/admin/version`（AdminAuth 之后，
  // 与本页同一档权限），不新开接口、不碰上游的 /api/status。
  //
  // retry: false —— 版本号是编译期常量，重试不可能得到不同结果；而扩展整体
  // 未启用时这里是稳定的 404，重试三次只是让这张卡多空白几秒。
  const qyVersionQuery = useQuery({
    queryKey: qyKeys.adminVersion(),
    queryFn: getQyVersion,
    staleTime: Infinity,
    retry: false,
  })

  // 扩展未启用（404）时整张卡消失，不弹错、不占位：这是 qy 一贯的"隐藏入口"
  // 降级，本页是上游页面，不该因为二开没装就多出一块红色报错。
  const qyVersion = qyVersionQuery.data
  const qyForkDeclared =
    qyVersion != null && qyVersion.fork !== QY_VERSION_UNINJECTED

  // 二开版本号（vX.Y.Z）与构建提交（git describe）是两个值，卡片上主次分明：
  // 大字是版本号（发版时人拍板的、能比大小的那个），小字是提交（排障用的）。
  let qyForkText = t('qy_maint_version_uninjected')
  let qyForkHint = t('qy_maint_version_uninjected_hint')
  if (qyVersion != null && qyForkDeclared) {
    qyForkText = qyVersion.fork
    const build =
      qyVersion.build === QY_VERSION_UNINJECTED
        ? t('qy_maint_version_uninjected')
        : qyVersion.build
    const upstream =
      qyVersion.upstream === QY_VERSION_UNINJECTED
        ? t('qy_maint_version_uninjected')
        : qyVersion.upstream
    qyForkHint = `${t('qy_maint_fork_build', { commit: build })} · ${t(
      'qy_maint_fork_based_on',
      { version: upstream }
    )}`
  }

  const handleCheckUpdates = async () => {
    setChecking(true)
    try {
      const response = await fetch(
        'https://api.github.com/repos/Calcium-Ion/new-api/releases/latest',
        {
          headers: {
            Accept: 'application/vnd.github+json',
            'User-Agent': 'new-api-dashboard',
          },
        }
      )

      if (!response.ok) {
        throw new Error(t('Failed to contact GitHub releases API'))
      }

      const data = (await response.json()) as ReleaseInfo
      if (!data?.tag_name) {
        throw new Error(t('Unexpected release payload'))
      }

      // 这条相等比较是内核版本号**必须与上游逐字一致**的原因：只要
      // common.Version 上带了任何二开后缀，这里就永远不相等，于是永远弹
      // "有新版本"，哪怕跑的就是最新的那一版。
      if (currentVersion && data.tag_name === currentVersion) {
        toast.success(
          t('You are running the latest version ({{version}}).', {
            version: data.tag_name,
          })
        )
        return
      }

      setRelease(data)
      setDialogOpen(true)
    } catch (error) {
      const message =
        error instanceof Error
          ? error.message
          : t('Failed to check for updates')
      toast.error(message)
    } finally {
      setChecking(false)
    }
  }

  const handleCheckForkUpdates = async () => {
    setForkChecking(true)
    setForkError(null)
    try {
      setForkResult(await checkQyUpdate())
    } catch (error) {
      setForkResult(null)
      setForkError({
        summary: qyErrorMessage(error, t),
        // rawMessage 是后端写的中文根因（具体的传输层错误 / 状态码 /
        // 还要等几分钟）。它不随语言切换，所以只能当细节行，不能当结论行。
        detail: isQyError(error) ? error.rawMessage : null,
      })
    } finally {
      setForkChecking(false)
    }
  }

  const goToRelease = () => {
    if (release?.html_url) {
      window.open(release.html_url, '_blank', 'noopener,noreferrer')
    }
  }

  return (
    <>
      <SettingsSection title={t('System maintenance')}>
        <div className='space-y-6'>
          {/* 列数跟着二开卡走：qy 扩展未启用时这里只有两张卡，硬写 3 列会留一块
              空洞。二开卡是异步到达的，届时 2→3 列重排一次。 */}
          <div
            className={
              qyVersion != null
                ? 'grid gap-4 md:grid-cols-3'
                : 'grid gap-4 md:grid-cols-2'
            }
          >
            <div className='rounded-lg border p-4'>
              <div className='text-muted-foreground text-sm'>
                {t('Current version')}
              </div>
              <div className='text-lg font-semibold'>{version}</div>
              {/* 这行副标题的唯一作用是在两个版本号并排时让人一眼分清哪个是
                  上游内核、哪个是二开。只有一张卡时它在解释一个不存在的对照物，
                  所以判定条件必须与二开卡逐字一致。 */}
              {qyVersion != null && (
                <div className='text-muted-foreground mt-1 text-xs'>
                  {t('qy_maint_core_version_hint')}
                </div>
              )}
            </div>
            {qyVersion != null && (
              <div className='rounded-lg border p-4'>
                <div className='text-muted-foreground text-sm'>
                  {t('qy_maint_fork_version')}
                </div>
                <div
                  className={
                    qyForkDeclared
                      ? 'text-lg font-semibold break-all'
                      : 'text-muted-foreground text-lg font-semibold'
                  }
                >
                  {qyForkText}
                </div>
                <div className='text-muted-foreground mt-1 text-xs break-all'>
                  {qyForkHint}
                </div>
              </div>
            )}
            <div className='rounded-lg border p-4'>
              <div className='text-muted-foreground text-sm'>
                {t('Uptime since')}
              </div>
              <div className='text-lg font-semibold'>{uptime}</div>
            </div>
          </div>

          {/* 两颗按钮各查各的更新源。标签必须点名查的是哪一个 ——
              并排两颗都叫"检查更新"，点下去谁也不知道弹出来的是谁的新版本。 */}
          <div className='flex flex-wrap items-center gap-3'>
            <Button onClick={handleCheckUpdates} disabled={checking}>
              {checking ? (
                t('Checking updates...')
              ) : (
                <>
                  <RefreshCcwIcon className='me-2 h-4 w-4' />
                  {t('qy_maint_check_core_update')}
                </>
              )}
            </Button>
            {qyVersion != null && (
              <Button
                variant='secondary'
                onClick={handleCheckForkUpdates}
                disabled={forkChecking}
              >
                {forkChecking ? (
                  t('Checking updates...')
                ) : (
                  <>
                    <RefreshCcwIcon className='me-2 h-4 w-4' />
                    {t('qy_maint_check_fork_update')}
                  </>
                )}
              </Button>
            )}
          </div>

          {forkError != null && (
            <div className='border-destructive/40 bg-destructive/5 space-y-1 rounded-lg border p-4'>
              <div className='text-destructive text-sm font-medium'>
                {forkError.summary}
              </div>
              {forkError.detail != null && (
                <div className='text-muted-foreground text-xs break-all'>
                  {forkError.detail}
                </div>
              )}
            </div>
          )}

          {forkResult != null && (
            <div className='space-y-2 rounded-lg border p-4'>
              <div className='text-sm font-medium'>
                {t(QY_UPDATE_STATUS_I18N[forkResult.status], {
                  current: forkResult.current,
                  latest: forkResult.latest,
                })}
              </div>
              {forkResult.prerelease && (
                <div className='text-muted-foreground text-xs'>
                  {t('qy_upd_prerelease')}
                </div>
              )}
              {forkResult.published_at !== '' && (
                <div className='text-muted-foreground text-xs'>
                  {t('Published')}{' '}
                  {formatTimestampToDate(
                    new Date(forkResult.published_at).getTime(),
                    'milliseconds'
                  )}
                </div>
              )}
              {/* 链接指向 release **页面**，绝不是下载直链：本站不自动下载、
                  不自动更新，升级是运维的动作。这句话必须写在界面上，
                  否则管理员会等着它自己装好。 */}
              <div className='text-muted-foreground text-xs'>
                {t('qy_upd_no_auto_download')}
              </div>
              <Button
                type='button'
                variant='outline'
                size='sm'
                onClick={() =>
                  window.open(
                    forkResult.release_url,
                    '_blank',
                    'noopener,noreferrer'
                  )
                }
              >
                <ExternalLinkIcon className='me-2 h-4 w-4' />
                {t('qy_upd_open_release')}
              </Button>
            </div>
          )}
        </div>
      </SettingsSection>

      <Dialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        title={
          release?.tag_name
            ? t('New version available: {{version}}', {
                version: release.tag_name,
              })
            : t('Release details')
        }
        description={
          release?.published_at
            ? `${t('Published')} ${formatTimestampToDate(
                new Date(release.published_at).getTime(),
                'milliseconds'
              )}`
            : undefined
        }
        contentClassName='max-h-[80vh] overflow-y-auto'
        contentHeight='auto'
        bodyClassName='space-y-4'
        footer={
          <>
            <Button
              type='button'
              variant='secondary'
              onClick={() => setDialogOpen(false)}
            >
              {t('Close')}
            </Button>
            {release?.html_url && (
              <Button type='button' onClick={goToRelease}>
                <ExternalLinkIcon className='me-2 h-4 w-4' />
                {t('Open release')}
              </Button>
            )}
          </>
        }
      >
        <div className='space-y-4'>
          {release?.body ? (
            <Markdown>{release.body}</Markdown>
          ) : (
            <p className='text-muted-foreground text-sm'>
              {t('No release notes provided.')}
            </p>
          )}
        </div>
      </Dialog>
    </>
  )
}
