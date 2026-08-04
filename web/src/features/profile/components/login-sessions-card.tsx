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
import { Logout01Icon, SmartPhone01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { Separator } from '@/components/ui/separator'
import { Skeleton } from '@/components/ui/skeleton'
import { QyPager } from '@/features/qy/pages/components/qy-pager'
import { clearAuthenticatedClientState } from '@/lib/api'
import type { LoginSession } from '@/stores/auth-store'

import {
  getLoginSessions,
  getQyLoginSessionStats,
  revokeLoginSession,
  revokeOtherLoginSessions,
} from '../api'
import { LoginSessionDialogs } from './login-session-dialogs'
import { LoginSessionItem } from './login-session-item'
import {
  buildLoginSessionsView,
  LOGIN_SESSIONS_PAGE_SIZE,
} from './login-session-utils'

const sessionQueryKey = ['profile', 'login-sessions'] as const

export function LoginSessionsCard() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [revokeTarget, setRevokeTarget] = useState<LoginSession | null>(null)
  const [confirmOthers, setConfirmOthers] = useState(false)
  const [requestedPage, setRequestedPage] = useState(1)
  // 到期判定要随时钟推进：页面挂着不动时，若只在首次渲染取一次 now，
  // "已到期"就永远停在那一刻的值，这个统计也就失去了意义。
  const [nowSeconds, setNowSeconds] = useState(() =>
    Math.floor(Date.now() / 1000)
  )
  useEffect(() => {
    const timer = setInterval(
      () => setNowSeconds(Math.floor(Date.now() / 1000)),
      60_000
    )
    return () => clearInterval(timer)
  }, [])

  const sessionsQuery = useQuery({
    queryKey: sessionQueryKey,
    queryFn: async () => {
      const response = await getLoginSessions()
      if (!response.success) {
        throw new Error(response.message || t('Failed to load login sessions'))
      }
      return response.data ?? []
    },
  })

  /**
   * 「已到期」只能从服务端拿。
   *
   * 上面那趟 getLoginSessions 走的是上游接口，它的 WHERE 带
   * `status='active' AND expires_at > now` —— 已到期的会话**结构性地不会下发**。
   * 拿它算出来的到期数恒为 0，那是在报告一个不可能非零的数字，比不显示更糟。
   *
   * 千夜端点查同一张表、不同的 WHERE。扩展未启用时返回 404 → qyGet 归类成
   * isHidden，这里静默回落到「只显示有效数」，不弹用户无从处理的错误，
   * 所以 retry:false（对一个稳定的 404 重试三次纯属浪费）。
   */
  const sessionStatsQuery = useQuery({
    queryKey: ['qy', 'profile', 'session-stats'] as const,
    queryFn: getQyLoginSessionStats,
    retry: false,
  })

  const revokeMutation = useMutation({
    mutationFn: async (sid: string) => {
      const response = await revokeLoginSession(sid)
      if (!response.success) {
        throw new Error(response.message || t('Failed to sign out session'))
      }
      return sid
    },
    onSuccess: async (sid) => {
      const revokedCurrent = sessionsQuery.data?.some(
        (session) => session.sid === sid && session.current
      )
      setRevokeTarget(null)
      if (revokedCurrent) {
        clearAuthenticatedClientState(queryClient)
        void navigate({ to: '/sign-in', replace: true })
        return
      }
      toast.success(t('Session signed out'))
      await queryClient.invalidateQueries({ queryKey: sessionQueryKey })
    },
    onError: (error: Error) => toast.error(error.message),
  })

  const revokeOthersMutation = useMutation({
    mutationFn: async () => {
      const response = await revokeOtherLoginSessions()
      if (!response.success) {
        throw new Error(
          response.message || t('Failed to sign out other sessions')
        )
      }
    },
    onSuccess: async () => {
      setConfirmOthers(false)
      toast.success(t('Other sessions signed out'))
      await queryClient.invalidateQueries({ queryKey: sessionQueryKey })
    },
    onError: (error: Error) => toast.error(error.message),
  })

  const sessions = sessionsQuery.data ?? []
  const hasOtherSessions = sessions.some((session) => !session.current)
  const view = buildLoginSessionsView(sessions, requestedPage, nowSeconds)
  // 夹紧只作用于本次渲染，state 不回写的话页码会"记住"一个已经不存在的旧值：
  // 撤销掉第 2 页最后一条 → 列表缩到 1 页、翻页条隐藏，看起来正常，但
  // requestedPage 还是 2；此后在别处登录让列表重新变长，夹紧不再生效，卡片会
  // 在用户没有任何操作的情况下自己跳回第 2 页。这个 effect 不是"用 useEffect 追
  // setState"——它只在夹紧真的发生时补一次对齐，本次渲染用的已经是夹紧后的值，
  // 不会多渲染一帧空列表，也一定在一轮内收敛。
  useEffect(() => {
    if (view.page !== requestedPage) setRequestedPage(view.page)
  }, [view.page, requestedPage])
  let sessionsContent: ReactNode
  if (sessionsQuery.isLoading) {
    sessionsContent = (
      <div className='flex flex-col gap-3'>
        <Skeleton className='h-20 w-full' />
        <Skeleton className='h-20 w-full' />
      </div>
    )
  } else if (sessionsQuery.isError) {
    sessionsContent = (
      <Empty>
        <EmptyHeader>
          <EmptyMedia variant='icon'>
            <HugeiconsIcon icon={SmartPhone01Icon} strokeWidth={2} />
          </EmptyMedia>
          <EmptyTitle>{t('Unable to load login sessions')}</EmptyTitle>
          <EmptyDescription>
            {t('Refresh the list and try again.')}
          </EmptyDescription>
        </EmptyHeader>
        <Button
          type='button'
          variant='outline'
          onClick={() => sessionsQuery.refetch()}
        >
          {t('Retry')}
        </Button>
      </Empty>
    )
  } else if (sessions.length === 0) {
    sessionsContent = (
      <Empty>
        <EmptyHeader>
          <EmptyMedia variant='icon'>
            <HugeiconsIcon icon={SmartPhone01Icon} strokeWidth={2} />
          </EmptyMedia>
          <EmptyTitle>{t('No active login sessions')}</EmptyTitle>
        </EmptyHeader>
      </Empty>
    )
  } else {
    sessionsContent = (
      <div className='flex flex-col'>
        {/* 「已到期」的权威来源是千夜端点，不是这份列表。
            上游 ListActiveUserSessions 的 WHERE 带 `expires_at > now`，
            已到期的会话根本不会下发，view.summary.expired 结构上只可能在
            「页面挂久了、缓存里某条在客户端时钟上过了期」这一种情况下非零。
            两者取大：服务端数字是库里的真相，客户端那份补上取数之后新过期的。
            千夜端点不可用（扩展未启用 → 404）时回落到只显示有效数，
            而不是摆一个恒零的到期数。 */}
        <p className='text-muted-foreground pb-1 text-xs'>
          {(() => {
            const serverExpired = sessionStatsQuery.data?.expired
            const expired =
              serverExpired == null
                ? view.summary.expired
                : Math.max(serverExpired, view.summary.expired)
            return expired > 0
              ? t('qy_login_sessions_summary_expired', {
                  active: view.summary.active,
                  expired,
                })
              : t('qy_login_sessions_summary', { active: view.summary.active })
          })()}
        </p>
        {view.visible.map((entry, index) => (
          <div key={entry.session.sid}>
            {index > 0 && <Separator />}
            <LoginSessionItem
              session={entry.session}
              expired={entry.expired}
              onRevoke={setRevokeTarget}
            />
          </div>
        ))}
        {/* 只有一页时整条隐藏：单页的"第 1/1 页"加两个灰按钮纯属噪声。 */}
        {view.pageCount > 1 && (
          <QyPager
            page={view.page}
            pageSize={LOGIN_SESSIONS_PAGE_SIZE}
            total={view.summary.total}
            onPageChange={setRequestedPage}
          />
        )}
      </div>
    )
  }

  return (
    <>
      <Card data-card-hover='false'>
        <CardHeader>
          <CardTitle>{t('Login sessions')}</CardTitle>
          <CardDescription>
            {t('Review and sign out devices currently using your account.')}
          </CardDescription>
          <CardAction>
            <Button
              type='button'
              variant='outline'
              size='sm'
              disabled={!hasOtherSessions || revokeOthersMutation.isPending}
              onClick={() => setConfirmOthers(true)}
            >
              <HugeiconsIcon
                icon={Logout01Icon}
                data-icon='inline-start'
                strokeWidth={2}
              />
              {t('Sign out other sessions')}
            </Button>
          </CardAction>
        </CardHeader>
        <CardContent>{sessionsContent}</CardContent>
      </Card>

      <LoginSessionDialogs
        revokeTarget={revokeTarget}
        confirmOthers={confirmOthers}
        revoking={revokeMutation.isPending}
        revokingOthers={revokeOthersMutation.isPending}
        onRevokeTargetChange={setRevokeTarget}
        onConfirmOthersChange={setConfirmOthers}
        onRevoke={() => {
          if (revokeTarget) revokeMutation.mutate(revokeTarget.sid)
        }}
        onRevokeOthers={() => revokeOthersMutation.mutate()}
      />
    </>
  )
}
