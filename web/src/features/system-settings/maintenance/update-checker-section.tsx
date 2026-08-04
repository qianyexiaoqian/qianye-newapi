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
import { qyKeys } from '@/features/qy/lib/query-keys'
import { getQyVersion } from '@/features/qy/pages/admin-health/api'
import { formatTimestamp, formatTimestampToDate } from '@/lib/format'

import { SettingsSection } from '../components/settings-section'

type ReleaseInfo = {
  tag_name: string
  name?: string
  body?: string
  html_url?: string
  published_at?: string
}

/**
 * 后端 `qianye/version.Unknown`：版本号未经构建期 ldflags 注入时的取值。
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

  const uptime = startTime ? formatTimestamp(startTime) : t('Unknown')
  const version = currentVersion || t('Unknown')

  // 二开版本。复用排障页那条 `GET /api/qy/admin/version`（AdminAuth 之后，与
  // 本页同一档权限），不新开接口、不碰上游的 /api/status。
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
  const qyBuildInjected =
    qyVersion != null && qyVersion.build !== QY_VERSION_UNINJECTED

  let qyBuildText = t('qy_maint_version_uninjected')
  let qyUpstreamText = t('qy_maint_version_uninjected_hint')
  if (qyVersion != null && qyBuildInjected) {
    qyBuildText = qyVersion.build
    // build 注入了而 upstream 没有，只可能是构建脚本被改坏；这时仍要说清
    // "基于哪个上游"这一栏是空的，而不是把整行藏掉让人以为没这回事。
    qyUpstreamText = t('qy_maint_fork_based_on', {
      version:
        qyVersion.upstream === QY_VERSION_UNINJECTED
          ? t('qy_maint_version_uninjected')
          : qyVersion.upstream,
    })
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
                    qyBuildInjected
                      ? 'text-lg font-semibold break-all'
                      : 'text-muted-foreground text-lg font-semibold'
                  }
                >
                  {qyBuildText}
                </div>
                <div className='text-muted-foreground mt-1 text-xs'>
                  {qyUpstreamText}
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

          <Button onClick={handleCheckUpdates} disabled={checking}>
            {checking ? (
              t('Checking updates...')
            ) : (
              <>
                <RefreshCcwIcon className='me-2 h-4 w-4' />
                {t('Check for updates')}
              </>
            )}
          </Button>
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
