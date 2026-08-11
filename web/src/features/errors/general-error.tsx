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
import { useNavigate, useRouter } from '@tanstack/react-router'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { copyToClipboard } from '@/lib/copy-to-clipboard'
import {
  claimStaleAssetReload,
  describeError,
  formatErrorReference,
  isStaleAssetError,
} from '@/lib/error-diagnostics'
import { cn } from '@/lib/utils'

const FEEDBACK_URL = 'https://github.com/QuantumNous/new-api/issues'

type GeneralErrorProps = React.HTMLAttributes<HTMLDivElement> & {
  minimal?: boolean
  error?: unknown
  /** TanStack Router 的 `errorComponent` 会传进来，用于重置错误边界。 */
  reset?: () => void
}

export function GeneralError({
  className,
  minimal = false,
  error,
  reset,
}: GeneralErrorProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const router = useRouter()
  const [retrying, setRetrying] = useState(false)

  const diagnostics = useMemo(() => describeError(error), [error])
  // 构建产物过期是本仓这个错误页最常见的来源：前端 go:embed 进二进制，重新构建后
  // chunk 的 hash 全变，老标签页拿着旧的 index.html 去取已经不存在的文件。
  const staleAssets = useMemo(() => isStaleAssetError(error), [error])
  const reference = useMemo(
    () =>
      error === undefined
        ? ''
        : formatErrorReference(diagnostics, {
            path: typeof window === 'undefined' ? '' : window.location.pathname,
            at: new Date(),
          }),
    [diagnostics, error]
  )

  // 版本换了就重新加载，这不是「用重试掩盖问题」——旧 chunk 已经不存在了，
  // 除了取新的 index.html 没有别的出路。冷却期内只自动做一次。
  useEffect(() => {
    if (!staleAssets || typeof window === 'undefined') return
    if (!claimStaleAssetReload(window.sessionStorage, Date.now())) return
    window.location.reload()
  }, [staleAssets])

  const isRateLimited = diagnostics.status === 429

  let title = `${t('Oops! Something went wrong')} ${`:')`}`
  let description = t('Please try again later.')
  if (staleAssets) {
    title = t('A new version has been released')
    description = t('Reload the page to get the latest version.')
  } else if (isRateLimited) {
    title = t('Too many requests')
    description = t('Please wait a moment before trying again.')
  }

  // 只在真的收到了 HTTP 状态码时才显示那个大字号数字。
  // 原来无论什么错误都印「500」——纯前端异常也印，用户照着去翻服务端日志，
  // 翻不到任何东西。项目方报的这个问题就是这么被藏住的。
  //
  // 没有 error 传进来时（/500 与 /errors/internal-server-error 两条路由把本组件
  // 当整页用）保留 500：那两处的语义本来就是「服务端 500」。
  const headline =
    error === undefined ? '500' : (diagnostics.status?.toString() ?? '')

  const retry = async () => {
    setRetrying(true)
    try {
      reset?.()
      await router.invalidate()
    } finally {
      setRetrying(false)
    }
  }

  return (
    <div className={cn('h-svh w-full', className)}>
      <div className='m-auto flex h-full w-full flex-col items-center justify-center gap-2 px-4'>
        {!minimal && headline && (
          <h1 className='text-[7rem] leading-tight font-bold'>{headline}</h1>
        )}
        <span className='font-medium'>{title}</span>
        <p className='text-muted-foreground text-center'>
          {t('We apologize for the inconvenience.')} <br /> {description}
        </p>
        {!minimal && reference && (
          <p className='text-muted-foreground/80 mt-2 max-w-xl text-center font-mono text-xs break-all'>
            {reference}
          </p>
        )}
        {!minimal && (
          <p className='text-muted-foreground text-center text-sm'>
            {t('If this keeps happening, please report it on GitHub Issues.')}
          </p>
        )}
        {!minimal && (
          <div className='mt-6 flex flex-wrap justify-center gap-4'>
            {staleAssets ? (
              <Button onClick={() => window.location.reload()}>
                {t('Reload')}
              </Button>
            ) : (
              <Button disabled={retrying} onClick={retry}>
                {t('Retry')}
              </Button>
            )}
            {reference && (
              <Button
                variant='outline'
                onClick={async () => {
                  const copied = await copyToClipboard(reference)
                  toast[copied ? 'success' : 'error'](
                    copied
                      ? t('Copied to clipboard')
                      : t('Failed to copy to clipboard')
                  )
                }}
              >
                {t('Copy error details')}
              </Button>
            )}
            <Button
              variant='outline'
              render={
                <a
                  href={FEEDBACK_URL}
                  target='_blank'
                  rel='noopener noreferrer'
                />
              }
            >
              {t('Report an issue')}
            </Button>
            <Button variant='outline' onClick={() => navigate({ to: '/' })}>
              {t('Back to Home')}
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}
