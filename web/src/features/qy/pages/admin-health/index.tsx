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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { RefreshCw, RotateCw, TriangleAlert } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { TitledCard } from '@/components/ui/titled-card'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyKeys } from '../../lib/query-keys'
import { QyStatGrid } from '../components/qy-stat-grid'
import { qyOpsErrorMessage } from '../ops/errors'
import {
  formatQyCount,
  formatQyDuration,
  formatQyMs,
  formatQyTs,
  QY_EMPTY_TEXT,
} from '../ops/format'
import { QyKeyValue } from '../ops/qy-ops-ui'
import {
  getQyAdminHealth,
  getQyClientIP,
  getQyOverdraft,
  getQyVersion,
  listQyLeases,
  reloadQyConfig,
} from './api'
import type {
  QyClientIPObservation,
  QyClientIPResolution,
  QyGroupRatioHealth,
  QyLeaseListItem,
  QyModuleSection,
  QyModuleSectionState,
} from './types'

/**
 * 一句话说清「这个 IP 是怎么来的」。
 *
 * 刻意把 reason 与 header 合成一句而不是各占一行：运维要的是结论
 * （「从 nginx 的 X-Forwarded-For 链里剥出来的」），两个机器可读的枚举
 * 分列两行需要人在脑子里再拼一次。
 */
function qyClientIPReasonText(
  resolution: QyClientIPResolution,
  t: TFunction
): string {
  const header = resolution.header ?? ''
  switch (resolution.reason) {
    case 'forwarded_chain':
      return t('qy_cfg_clientip_rs_chain', { header })
    case 'forwarded_chain_all_trusted':
      return t('qy_cfg_clientip_rs_all_trusted', { header })
    case 'forwarded_header':
      return t('qy_cfg_clientip_rs_header', { header })
    case 'trusted_peer_no_header':
      return t('qy_cfg_clientip_rs_no_header')
    case 'peer_unparsable':
      return t('qy_cfg_clientip_rs_unparsable')
    default:
      return t('qy_cfg_clientip_rs_direct')
  }
}

/** 需要处理的两种状态：模块编译进来了，但配置里没人对它做过决定。 */
const QY_MODULE_STATES_NEEDING_ATTENTION: ReadonlySet<QyModuleSectionState> =
  new Set(['missing_section', 'missing_key'])

const QY_MODULE_STATE_LABELS: Record<QyModuleSectionState, string> = {
  declared: 'qy_cfg_health_module_st_declared',
  missing_section: 'qy_cfg_health_module_st_missing_section',
  missing_key: 'qy_cfg_health_module_st_missing_key',
  default_on: 'qy_cfg_health_module_st_default_on',
  ungated: 'qy_cfg_health_module_st_ungated',
}

/**
 * 只有两种 missing 是 warning，其余一律中性。
 *
 * 刻意不把 `enabled: false` 标成任何颜色：关掉一个功能是运维的正当选择，
 * 给它上色会让这张表在正常站点上也满屏发黄，两周之后就没人再看它 ——
 * 而它唯一的作用就是被人看见。
 */
function qyModuleStateVariant(state: QyModuleSectionState) {
  return QY_MODULE_STATES_NEEDING_ATTENTION.has(state)
    ? ('warning' as const)
    : ('neutral' as const)
}

/**
 * 分组倍率失配的一句话结论。没有任何需要处理的事情时返回空串。
 *
 * 两个信号合成一句：
 *
 *   - `orphan_users > 0`  **正在发生的资损**。这些用户的分组不在倍率表里，
 *     上游 `GetGroupRatio` 会 fail-open 按 1.0 倍扣他们的钱，没有任何拒绝。
 *   - `observed`          扩展自己解析倍率时撞到过的失配名。它覆盖面更窄，
 *     但能证明「已经真的发生过」，而不只是「理论上会」。
 *
 * 刻意**只在非零时才出现**：一个在正常站点上也常驻的告警，两周之后就没人再看它 ——
 * 而它唯一的作用就是被人看见。这与本页模块段刻意不给 `enabled: false` 上色同理。
 *
 * 扫描从来没跑过（`last_scan` 缺失）时不喊:那是「还不知道」，不是「有问题」。
 * 想知道就去 `GET /admin/group-ratio/orphans`。
 */
function qyGroupRatioAlert(
  health: QyGroupRatioHealth | undefined,
  t: TFunction
): string {
  if (health == null) return ''

  const orphanUsers = health.last_scan?.orphan_users ?? 0
  const names = health.last_scan?.orphans ?? []
  if (orphanUsers > 0) {
    return t('qy_cfg_health_group_ratio_users', {
      users: orphanUsers,
      groups: names.map((o) => o.group).join(', '),
    })
  }
  if (health.observed.length > 0) {
    return t('qy_cfg_health_group_ratio_observed', {
      groups: health.observed.map((m) => m.group).join(', '),
    })
  }
  return ''
}

/**
 * 扩展健康面板。
 *
 * 排障的第一入口：数据库通不通、熔断开没开、连接池是不是打满、后台任务的
 * 租约在谁手里、热路径队列有没有丢事件、配置是从哪个文件加载的。
 *
 * **队列丢弃数是这一页最重要的一个数字**：它是全扩展唯一会造成
 * 「用户该拿的钱没拿到」的路径，非零必须红字告警。
 */
export function QyAdminHealth() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [reloadOpen, setReloadOpen] = useState(false)

  const healthQuery = useQuery({
    queryKey: qyKeys.adminHealth(),
    queryFn: getQyAdminHealth,
    staleTime: 10_000,
    refetchInterval: 30_000,
  })

  const leasesQuery = useQuery({
    queryKey: qyKeys.adminLeases(),
    queryFn: listQyLeases,
    staleTime: 10_000,
    refetchInterval: 30_000,
  })

  /**
   * 负余额（透支）总览。
   *
   * 本站**刻意接受**预扣/结算无下限（拍板与代价见 `qianye/docs/decisions.md`
   * D-01）。接受不等于不管：这张卡就是那条取舍的运维面 —— 现在有多少账号
   * 是负的、合计欠多少、最深的是谁。它只读，处置动作在上游用户管理页。
   *
   * 30 秒轮询与健康数据同频：欠款是会在几分钟内变化的量，一个手动刷新才更新
   * 的数字会让人拿着上一次打开页面时的结论去做处置决定。
   */
  const overdraftQuery = useQuery({
    queryKey: qyKeys.adminOverdraft(),
    queryFn: getQyOverdraft,
    staleTime: 10_000,
    refetchInterval: 30_000,
  })

  /**
   * 客户端 IP 识别诊断。
   *
   * 每次打开都重取（staleTime 0）而不是缓存：这一页的 `request` 段说的是
   * **这一条请求**被识别成了什么，缓存住等于把上一次访问的结论当成现在的
   * 结论。而运维改完 `TRUSTED_PROXIES` 重启进程之后，第一件事就是回到这里
   * 确认策略真的换了。
   *
   * 不轮询：策略是启动时定死的，只有观测台的计数会涨，而那个数涨不涨不影响
   * 任何判断 —— 非零就是非零。
   */
  const clientIPQuery = useQuery({
    queryKey: qyKeys.adminClientIP(),
    queryFn: getQyClientIP,
    staleTime: 0,
  })

  // 版本是编译期常量，进程不重启就不会变，所以既不轮询也不过期。
  // 刷新按钮仍然带上它：那是「部署到底生效没有」在同一页里的确认方式。
  const versionQuery = useQuery({
    queryKey: qyKeys.adminVersion(),
    queryFn: getQyVersion,
    staleTime: Infinity,
  })

  const reloadMutation = useMutation({
    mutationFn: reloadQyConfig,
    onSuccess: () => {
      toast.success(t('qy_cfg_health_reload_done'))
      setReloadOpen(false)
      void queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const health = healthQuery.data
  const now = Math.floor(Date.now() / 1000)
  const dropped = health?.hot_queue.dropped ?? 0
  const breakerOpen = (health?.db.breaker_open_until ?? 0) > now
  const uncertain = health?.two_phase.uncertain ?? 0
  // 旧版本后端不下发 modules，`?? []` 让这一页在混版部署时照常打开。
  const modules = health?.modules ?? []
  const silentModules = modules.filter((m) =>
    QY_MODULE_STATES_NEEDING_ATTENTION.has(m.state)
  )
  const groupRatioAlert = qyGroupRatioAlert(health?.group_ratio, t)

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_cfg_health_title')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={healthQuery.isFetching}
          onClick={() => {
            void healthQuery.refetch()
            void leasesQuery.refetch()
            void versionQuery.refetch()
            void overdraftQuery.refetch()
            void clientIPQuery.refetch()
          }}
        >
          <RefreshCw
            aria-hidden='true'
            className={healthQuery.isFetching ? 'animate-spin' : undefined}
          />
          {t('qy_common_refresh')}
        </Button>
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={() => setReloadOpen(true)}
        >
          <RotateCw aria-hidden='true' />
          {t('qy_cfg_health_reload')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <div className='space-y-3'>
          {/* 版本卡刻意放在 QyPageBoundary **外面**，而不是接进下面
              「两阶段与节点」那张卡里。

              理由是后端把 `/admin/version` 单独拆出来的那条：扩展库不可用时
              `/admin/health` 会 503，boundary 随即把整个内容区换成错误态 ——
              而那正是最需要先确认「现在跑的是哪个版本」的时刻。放进 boundary
              里等于把这个能力白拆一遍。

              `versionQuery.data != null` 而不是渲染错误态：扩展整体被关掉时
              这个请求会 404，此时下面的 boundary 已经在解释原因了，这里再叠一个
              红框只是噪声，安静消失即可。 */}
          {versionQuery.data != null && (
            <TitledCard
              title={t('qy_cfg_health_version')}
              description={t('qy_cfg_health_version_desc')}
            >
              {/* 顺序是刻意的:两个**版本号**在前(内核 / 二开,各说各的事),
                  两个**定位用的提交**在后。曾经这里只有三行、而且把
                  `<上游 tag>+qy.<轮次>` 合成的那一个值叫「上游内核版本」——
                  那一栏于是既不是上游版本也不是我们的版本。 */}
              <QyKeyValue label={t('qy_cfg_health_version_core')}>
                {versionQuery.data.core}
              </QyKeyValue>
              <QyKeyValue label={t('qy_cfg_health_version_fork')}>
                {versionQuery.data.fork}
              </QyKeyValue>
              <QyKeyValue label={t('qy_cfg_health_version_upstream')}>
                {versionQuery.data.upstream}
              </QyKeyValue>
              <QyKeyValue label={t('qy_cfg_health_version_build')}>
                {versionQuery.data.build}
              </QyKeyValue>
            </TitledCard>
          )}

          {/* 透支总览刻意放在 QyPageBoundary **外面**，理由与上面版本卡那条
              一模一样：后端 `/admin/overdraft` 不走 requireCore（只查主库
              users），扩展库不可用时它仍然是 200，而 `/admin/health` 会 503，
              boundary 随即把整个内容区换成错误态。放进去等于让"站上现在欠了
              多少钱"跟着扩展库一起消失。

              `overdraftQuery.data != null` 而不是渲染错误态：扩展整体被关掉时
              这个请求会 404，此时下面的 boundary 已经在解释原因了。 */}
          {overdraftQuery.data != null && (
            <TitledCard
              title={t('qy_cfg_overdraft_title')}
              description={t('qy_cfg_overdraft_desc')}
            >
              <QyStatGrid
                items={[
                  {
                    key: 'accounts',
                    label: t('qy_cfg_overdraft_accounts'),
                    value: (
                      <span
                        className={
                          overdraftQuery.data.accounts > 0
                            ? 'text-warning'
                            : 'text-success'
                        }
                      >
                        {formatQyCount(overdraftQuery.data.accounts)}
                      </span>
                    ),
                    hint: t('qy_cfg_overdraft_accounts_hint'),
                  },
                  {
                    key: 'owed',
                    label: t('qy_cfg_overdraft_total'),
                    // 合计欠额下发的就是正数（后端 Report.TotalOwed 恒 >= 0，
                    // 取反只在那一处做过一次），所以这里**原样**渲染:再取一次
                    // 反会在界面上写出一个负的欠款额，而它旁边的"欠得最深"
                    // (deepest.quota，库里本来就是负的) 同符号同量级，两个数会被
                    // 读成同一个。方向由标签"合计欠额"表达，颜色由 text-warning
                    // 表达 —— 不借 QyAmountText 的负数红色语义。
                    value: (
                      <QyAmountText
                        quota={overdraftQuery.data.total_owed}
                        variant='hero'
                        className={
                          overdraftQuery.data.total_owed > 0
                            ? 'text-warning'
                            : undefined
                        }
                      />
                    ),
                    hint: t('qy_cfg_overdraft_total_hint'),
                    emphasis: true,
                  },
                  {
                    key: 'deepest',
                    label: t('qy_cfg_overdraft_deepest'),
                    value:
                      overdraftQuery.data.deepest == null ? (
                        QY_EMPTY_TEXT
                      ) : (
                        <QyAmountText
                          quota={overdraftQuery.data.deepest.quota}
                          variant='hero'
                        />
                      ),
                    hint:
                      overdraftQuery.data.deepest == null
                        ? t('qy_cfg_overdraft_none')
                        : `${overdraftQuery.data.deepest.username} (#${overdraftQuery.data.deepest.user_id})`,
                  },
                ]}
              />
              {overdraftQuery.data.top.length > 0 && (
                <div className='mt-3'>
                  {overdraftQuery.data.top.map((account) => (
                    <QyKeyValue
                      key={account.user_id}
                      label={`${account.username} (#${account.user_id}) · ${account.group}`}
                    >
                      <QyAmountText quota={account.quota} />
                    </QyKeyValue>
                  ))}
                  {overdraftQuery.data.truncated && (
                    <p className='text-muted-foreground mt-2 text-xs'>
                      {t('qy_cfg_overdraft_truncated', {
                        shown: overdraftQuery.data.top.length,
                        total: overdraftQuery.data.accounts,
                      })}
                    </p>
                  )}
                </div>
              )}
            </TitledCard>
          )}

          {/* 客户端 IP 识别。刻意放在 QyPageBoundary **外面**，理由与版本卡、
              透支卡那两条一模一样：后端 `/admin/client-ip` 不走 requireCore
              （只读进程内的取值策略与本次请求自身），扩展库不可用时它仍然
              是 200。而令牌 allow_ips 与按 IP 的限流根本不依赖扩展库 ——
              它们照样在按这个值放行/拒绝，这份诊断绝不该跟着扩展库一起消失。

              这一页回答的不是「我的 IP 是什么」（台账里就有），而是
              **「为什么是这个值」**：直连对端是谁、它落在哪一档受信网段里、
              结论从哪个头取的、哪些头被丢掉了。少了这几项，运维只能在
              TRUSTED_PROXIES 里试错，而每次试错都要重启进程。 */}
          {clientIPQuery.data != null && (
            <TitledCard
              title={t('qy_cfg_clientip_title')}
              description={t('qy_cfg_clientip_desc')}
            >
              {/* 观测台非空基本等同于确诊：站点前面确实有反代/CDN，但
                  TRUSTED_PROXIES 没配到那个对端上。这条告警必须给出可以
                  直接粘贴的值 —— 只说「配置有问题」等于把人推回去猜。 */}
              {clientIPQuery.data.observations.length > 0 && (
                <Alert variant='destructive' className='mb-3'>
                  <TriangleAlert />
                  <AlertTitle>{t('qy_cfg_clientip_obs_title')}</AlertTitle>
                  <AlertDescription>
                    {t('qy_cfg_clientip_obs_desc', {
                      value: clientIPQuery.data.observations
                        .map((o: QyClientIPObservation) => o.suggestion)
                        .filter((v: string) => v !== '')
                        .join(','),
                    })}
                  </AlertDescription>
                </Alert>
              )}
              {clientIPQuery.data.policy.warning !== '' && (
                <Alert variant='destructive' className='mb-3'>
                  <TriangleAlert />
                  <AlertTitle>{t('qy_cfg_clientip_warn_title')}</AlertTitle>
                  <AlertDescription>
                    {clientIPQuery.data.policy.warning}
                  </AlertDescription>
                </Alert>
              )}

              <QyKeyValue label={t('qy_cfg_clientip_ip')}>
                <span className='font-mono'>
                  {clientIPQuery.data.request.ip === ''
                    ? QY_EMPTY_TEXT
                    : clientIPQuery.data.request.ip}
                </span>
              </QyKeyValue>
              <QyKeyValue label={t('qy_cfg_clientip_peer')}>
                <span className='font-mono'>
                  {clientIPQuery.data.request.peer === ''
                    ? QY_EMPTY_TEXT
                    : clientIPQuery.data.request.peer}
                </span>
              </QyKeyValue>
              <QyKeyValue label={t('qy_cfg_clientip_peer_trusted')}>
                <StatusBadge
                  label={
                    clientIPQuery.data.request.peer_trusted
                      ? t('qy_cfg_clientip_peer_trusted_yes', {
                          source: clientIPQuery.data.request.trust_source ?? '',
                        })
                      : t('qy_cfg_clientip_peer_trusted_no')
                  }
                  variant={
                    clientIPQuery.data.request.peer_trusted
                      ? 'success'
                      : 'neutral'
                  }
                  copyable={false}
                />
              </QyKeyValue>
              <QyKeyValue label={t('qy_cfg_clientip_reason')}>
                {qyClientIPReasonText(clientIPQuery.data.request, t)}
              </QyKeyValue>
              <QyKeyValue label={t('qy_cfg_clientip_strategy')}>
                {t(`qy_cfg_clientip_st_${clientIPQuery.data.request.strategy}`)}
                {clientIPQuery.data.policy.raw !== '' && (
                  <span className='text-muted-foreground ml-2 font-mono text-xs'>
                    TRUSTED_PROXIES={clientIPQuery.data.policy.raw}
                  </span>
                )}
              </QyKeyValue>
              {(clientIPQuery.data.request.chain ?? []).length > 0 && (
                <QyKeyValue label={t('qy_cfg_clientip_chain')}>
                  {/* 原文按序展示，不做归一化：排障时要看到反代**实际发过来**
                      的字符串，归一化之后 `1.2.3.4:5678` 与 `::ffff:1.2.3.4`
                      这类形态就看不见了，而它们恰恰是最容易出问题的那些。
                      左端是客户端自己写的、谁都能编的前缀。 */}
                  <span className='font-mono text-xs'>
                    {(clientIPQuery.data.request.chain ?? []).join(' -> ')}
                  </span>
                </QyKeyValue>
              )}
              {(clientIPQuery.data.request.conflicts ?? []).length > 0 && (
                <QyKeyValue label={t('qy_cfg_clientip_conflict')}>
                  {/* 另一个受信请求头给出了不同的答案。最常见的成因是
                      「只配了 X-Real-IP 的 Nginx」:那种 nginx 原样透传客户端
                      自带的 X-Forwarded-For,而默认头顺序里 XFF 排在前面,
                      于是客户端能顶掉反代诚实写下的值。修法是显式声明
                      CLIENT_IP_HEADERS=X-Real-IP。 */}
                  <span className='text-warning font-mono text-xs'>
                    {(clientIPQuery.data.request.conflicts ?? [])
                      .map((h) => `${h.header}: ${h.ip}`)
                      .join(' | ')}
                  </span>
                </QyKeyValue>
              )}
              {(clientIPQuery.data.request.ignored_headers ?? []).length >
                0 && (
                <QyKeyValue label={t('qy_cfg_clientip_ignored')}>
                  <span className='font-mono text-xs'>
                    {(clientIPQuery.data.request.ignored_headers ?? [])
                      .map((h) => `${h.name}: ${h.value}`)
                      .join(' | ')}
                  </span>
                </QyKeyValue>
              )}
              {clientIPQuery.data.policy.sources.map((source) => (
                <QyKeyValue
                  key={source.name}
                  label={t('qy_cfg_clientip_source', { name: source.name })}
                >
                  <span className='font-mono text-xs'>
                    {source.headers.join(', ')} &lt;- {source.cidrs.join(', ')}
                  </span>
                </QyKeyValue>
              ))}
              {clientIPQuery.data.observations.map(
                (observation: QyClientIPObservation) => (
                  <QyKeyValue
                    key={observation.peer}
                    label={t('qy_cfg_clientip_obs_row', {
                      peer: observation.peer,
                    })}
                  >
                    <span className='font-mono text-xs'>
                      {observation.suggestion} ·{' '}
                      {formatQyCount(observation.count)} ·{' '}
                      {observation.headers.join(', ')} ·{' '}
                      {formatQyTs(observation.last_seen)}
                    </span>
                  </QyKeyValue>
                )
              )}
              {clientIPQuery.data.observations_dropped > 0 && (
                <p className='text-muted-foreground mt-2 text-xs'>
                  {t('qy_cfg_clientip_obs_dropped', {
                    n: clientIPQuery.data.observations_dropped,
                  })}
                </p>
              )}
            </TitledCard>
          )}

          <QyPageBoundary query={healthQuery}>
            {health != null && (
              <div className='space-y-3'>
                {/* 丢弃 = 佣金 / 违规记录 / 采样事件被永久丢掉，无法补回。 */}
                {dropped > 0 && (
                  <Alert variant='destructive'>
                    <TriangleAlert />
                    <AlertTitle>{t('qy_cfg_health_drop_title')}</AlertTitle>
                    <AlertDescription>
                      {t('qy_cfg_health_drop_desc', { n: dropped })}
                    </AlertDescription>
                  </Alert>
                )}
                {/* 「代码全都编译进去了，刷新却看不到功能」在这一页的落点。
                    启动时同一件事会打一条 [SYS] 告警，但启动日志会被滚走，
                    而排障的人往往是在事后才来看这里。 */}
                {silentModules.length > 0 && (
                  <Alert variant='destructive'>
                    <TriangleAlert />
                    <AlertTitle>
                      {t('qy_cfg_health_modules_alert_title')}
                    </AlertTitle>
                    <AlertDescription>
                      {/* 列的是开关路径而不是模块名：一个模块可能有好几个开关，
                          只报模块名会让人补上总开关就以为完事了，而 violation
                          真正决定「抓不抓」的是段内那两个二级开关。 */}
                      {t('qy_cfg_health_modules_alert_desc', {
                        list: silentModules
                          .map((m) => `${m.section}.${m.key}`)
                          .join(', '),
                      })}
                    </AlertDescription>
                  </Alert>
                )}
                {/* 上游 GetGroupRatio 的 fail-open 在这一页的落点。
                    它查不到分组时返回 1、只写一行会被滚走的 SysLog，于是
                    「某个免费分组被改名或删掉」的表现是静默按 1.0 倍扣费。
                    这条告警是它唯一常驻的信号。 */}
                {groupRatioAlert !== '' && (
                  <Alert variant='destructive'>
                    <TriangleAlert />
                    <AlertTitle>
                      {t('qy_cfg_health_group_ratio_title')}
                    </AlertTitle>
                    <AlertDescription>{groupRatioAlert}</AlertDescription>
                  </Alert>
                )}
                {breakerOpen && (
                  <Alert variant='destructive'>
                    <TriangleAlert />
                    <AlertTitle>{t('qy_cfg_health_breaker_title')}</AlertTitle>
                    <AlertDescription>
                      {t('qy_cfg_health_breaker_desc', {
                        time: formatQyTs(health.db.breaker_open_until),
                      })}
                    </AlertDescription>
                  </Alert>
                )}

                <QyStatGrid
                  items={[
                    {
                      key: 'db',
                      label: t('qy_cfg_health_db'),
                      value: (
                        <StatusBadge
                          label={
                            health.db.available
                              ? t('qy_cfg_health_db_up')
                              : t('qy_cfg_health_db_down')
                          }
                          variant={health.db.available ? 'success' : 'danger'}
                          copyable={false}
                          size='md'
                        />
                      ),
                      hint: t('qy_cfg_health_ping', {
                        ms: formatQyMs(health.db.last_ping_ms),
                      }),
                    },
                    {
                      key: 'queue',
                      label: t('qy_cfg_health_queue'),
                      value: `${health.hot_queue.pending} / ${health.hot_queue.capacity}`,
                      hint: t('qy_cfg_health_submitted', {
                        n: formatQyCount(health.hot_queue.submitted),
                      }),
                    },
                    {
                      key: 'dropped',
                      label: t('qy_cfg_health_dropped'),
                      // 丢弃是本页最重要的一个数字：非零意味着有影响资金的
                      // 事件被永久丢掉，必须一眼看到。
                      value: (
                        <span
                          className={
                            dropped > 0 ? 'text-destructive' : 'text-success'
                          }
                        >
                          {formatQyCount(dropped)}
                        </span>
                      ),
                      hint: t('qy_cfg_health_dropped_hint'),
                      emphasis: true,
                    },
                    {
                      key: 'uncertain',
                      label: t('qy_cfg_health_uncertain'),
                      value: (
                        <span
                          className={uncertain > 0 ? 'text-warning' : undefined}
                        >
                          {formatQyCount(uncertain)}
                        </span>
                      ),
                      hint: t('qy_cfg_health_uncertain_hint'),
                    },
                  ]}
                />

                <div className='grid gap-3 lg:grid-cols-2'>
                  <TitledCard title={t('qy_cfg_health_db_pool')}>
                    <QyKeyValue label={t('qy_cfg_health_connected')}>
                      {health.db.connected
                        ? t('qy_cfg_health_yes')
                        : t('qy_cfg_health_no')}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_cfg_health_fail_streak')}>
                      {health.db.fail_streak}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_cfg_health_last_ping_at')}>
                      {formatQyTs(health.db.last_ping_at)}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_cfg_health_open_conns')}>
                      {health.db.open_conns == null
                        ? QY_EMPTY_TEXT
                        : `${health.db.open_conns} / ${health.db.max_open ?? 0}`}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_cfg_health_in_use')}>
                      {health.db.in_use ?? QY_EMPTY_TEXT}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_cfg_health_idle')}>
                      {health.db.idle ?? QY_EMPTY_TEXT}
                    </QyKeyValue>
                    {/* wait_count 持续增长 = 连接池太小，请求在排队等连接。 */}
                    <QyKeyValue label={t('qy_cfg_health_wait_count')}>
                      {health.db.wait_count ?? QY_EMPTY_TEXT}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_cfg_health_tables')}>
                      {health.migrate.table_count}
                    </QyKeyValue>
                  </TitledCard>

                  <TitledCard title={t('qy_cfg_health_two_phase')}>
                    <QyKeyValue label={t('qy_common_st_pending')}>
                      {health.two_phase.pending ?? QY_EMPTY_TEXT}
                    </QyKeyValue>
                    {/* in_doubt = 主库 COMMIT 已发出、结局不明。它和 pending
                        长得像但含义完全不同：钱很可能已经动了，只是还没人能证明。
                        大于 0 就标警示色，否则它会消失在最常见的那一档里。 */}
                    <QyKeyValue label={t('qy_common_st_in_doubt')}>
                      <span
                        className={
                          (health.two_phase.in_doubt ?? 0) > 0
                            ? 'text-warning'
                            : undefined
                        }
                      >
                        {health.two_phase.in_doubt ?? QY_EMPTY_TEXT}
                      </span>
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_common_st_uncertain')}>
                      {health.two_phase.uncertain ?? QY_EMPTY_TEXT}
                    </QyKeyValue>
                    {/* 两组「最老的一笔」各自带上自己的单号。
                        原先只有一个「单号」行、取的还是未落定那一路，于是
                        待人工裁决那一档给出了时长却给不出单号 —— 而对账台按
                        id desc 排序，最老的那张恰好在最后一页。 */}
                    <QyKeyValue label={t('qy_cfg_health_oldest_uncertain')}>
                      {formatQyDuration(
                        health.two_phase.oldest_uncertain_age_sec
                      )}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_cfg_health_oldest_uncertain_no')}>
                      {health.two_phase.oldest_uncertain_order_no ??
                        QY_EMPTY_TEXT}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_cfg_health_oldest_unsettled')}>
                      {formatQyDuration(
                        health.two_phase.oldest_unsettled_age_sec
                      )}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_cfg_health_oldest_unsettled_no')}>
                      {health.two_phase.oldest_unsettled_order_no ??
                        QY_EMPTY_TEXT}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_cfg_health_node')}>
                      {health.node.name}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_cfg_health_is_master')}>
                      {health.node.is_master
                        ? t('qy_cfg_health_yes')
                        : t('qy_cfg_health_no')}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_cfg_health_config_path')}>
                      {health.config.path}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_cfg_health_config_loaded')}>
                      {formatQyTs(health.config.loaded_at)}
                    </QyKeyValue>
                  </TitledCard>
                </div>

                <TitledCard
                  title={t('qy_cfg_health_modules')}
                  description={t('qy_cfg_health_modules_desc')}
                >
                  <StaticDataTable
                    data={modules}
                    // 一个模块可能占多行（总开关 + 二级开关），行 key 必须带上
                    // key 那一列，否则 violation 的三行会共用同一个 React key。
                    getRowKey={(row) => `${row.module}.${row.key}`}
                    emptyContent={t('qy_cfg_health_modules_empty')}
                    columns={[
                      {
                        id: 'module',
                        header: t('qy_cfg_health_module_name'),
                        cell: (row: QyModuleSection) => row.module,
                      },
                      {
                        id: 'section',
                        header: t('qy_cfg_health_module_section'),
                        cell: (row: QyModuleSection) =>
                          row.section === ''
                            ? QY_EMPTY_TEXT
                            : `${row.section}.${row.key === '' ? '*' : row.key}`,
                      },
                      {
                        id: 'state',
                        header: t('qy_common_status'),
                        cell: (row: QyModuleSection) => (
                          <StatusBadge
                            label={t(QY_MODULE_STATE_LABELS[row.state])}
                            variant={qyModuleStateVariant(row.state)}
                            copyable={false}
                          />
                        ),
                      },
                      {
                        id: 'enabled',
                        header: t('qy_cfg_health_module_enabled'),
                        cell: (row: QyModuleSection) =>
                          row.enabled
                            ? t('qy_cfg_health_yes')
                            : t('qy_cfg_health_no'),
                      },
                      {
                        id: 'effect',
                        header: t('qy_cfg_health_module_effect'),
                        cell: (row: QyModuleSection) => (
                          <div className='space-y-1'>
                            <p className='text-muted-foreground text-xs'>
                              {row.effect}
                            </p>
                            {/* fix 只在两种 missing 状态下由后端给出。
                                必须连「往哪儿粘」一起说：missing_key 的前提是
                                那一段已经在文件里了，把片段当成新的顶层段追加
                                会产生重复的顶层 YAML 键 —— 配置从此解析失败，
                                整台网关起不来。一条不阻断启动的告警，其修复
                                指引反而把网关关停了。 */}
                            {row.fix != null && row.fix !== '' && (
                              <>
                                <p className='text-muted-foreground text-xs'>
                                  {row.state === 'missing_key'
                                    ? t('qy_cfg_health_module_fix_key', {
                                        section: row.section,
                                      })
                                    : t('qy_cfg_health_module_fix_section')}
                                </p>
                                <pre className='bg-muted overflow-x-auto rounded px-2 py-1 text-xs'>
                                  {row.fix}
                                </pre>
                              </>
                            )}
                          </div>
                        ),
                      },
                    ]}
                  />
                </TitledCard>

                <TitledCard
                  title={t('qy_cfg_health_leases')}
                  description={t('qy_cfg_health_leases_desc')}
                >
                  <StaticDataTable
                    data={leasesQuery.data?.items ?? []}
                    getRowKey={(row) => row.name}
                    emptyContent={t('qy_cfg_health_leases_empty')}
                    columns={[
                      {
                        id: 'name',
                        header: t('qy_cfg_health_lease_name'),
                        cell: (row: QyLeaseListItem) => row.name,
                      },
                      {
                        id: 'holder',
                        header: t('qy_cfg_health_lease_holder'),
                        cell: (row: QyLeaseListItem) =>
                          row.holder === leasesQuery.data?.self
                            ? `${row.holder} (${t('qy_cfg_health_lease_self')})`
                            : row.holder,
                      },
                      {
                        id: 'fence',
                        header: t('qy_cfg_health_lease_fence'),
                        cellClassName: 'tabular-nums',
                        cell: (row: QyLeaseListItem) => row.fence,
                      },
                      {
                        id: 'lease_until',
                        header: t('qy_cfg_health_lease_until'),
                        cell: (row: QyLeaseListItem) =>
                          formatQyTs(row.lease_until),
                      },
                      {
                        id: 'expired',
                        header: t('qy_common_status'),
                        cell: (row: QyLeaseListItem) => (
                          <StatusBadge
                            label={
                              row.expired
                                ? t('qy_cfg_health_lease_expired')
                                : t('qy_cfg_health_lease_held')
                            }
                            variant={row.expired ? 'warning' : 'success'}
                            copyable={false}
                          />
                        ),
                      },
                    ]}
                  />
                </TitledCard>

                <QyConfirmDialog
                  open={reloadOpen}
                  onOpenChange={setReloadOpen}
                  title={t('qy_cfg_health_reload')}
                  description={t('qy_cfg_health_reload_desc')}
                  confirmText={t('qy_cfg_health_reload')}
                  isLoading={reloadMutation.isPending}
                  onConfirm={() => reloadMutation.mutate()}
                />
              </div>
            )}
          </QyPageBoundary>
        </div>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
