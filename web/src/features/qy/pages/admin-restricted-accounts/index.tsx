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
import { Link } from '@tanstack/react-router'
import { Ban, Check, Users } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { USER_STATUS } from '@/features/users/constants'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyAdminRestrictedAccountsQuery } from './api'
import { QyRestrictedNoticeCard } from './components/notice-card'

/**
 * 上游用户列表 `status` 搜索参数里表示「已禁用」的那一档。
 *
 * 类型标成 `` `${typeof USER_STATUS.DISABLED}` `` 而不是直接写 `'2'`：
 * 路由的 `validateSearch` 只收 `'-1' | '1' | '2'`，所以 `String(...)` 那种写法
 * 会退化成 `string` 而报错；而裸写 `'2'` 又与 `USER_STATUS` 脱钩，将来上游改了
 * 常量这里不会有任何信号。绑在一起之后，两者对不上是**编译期**错误。
 */
const DISABLED_STATUS_PARAM: `${typeof USER_STATUS.DISABLED}` = '2'

/**
 * 「系统设置 → 扩展设置 → 受限账号」。
 *
 * ── 项目方原话 ──
 * 「受限制账号，在系统设置里面单独进行配置。」
 *
 * ── 这一页收了什么、为什么只收这些 ──
 *
 * 收进来的三块：
 *   ① 现状 —— 当前有多少个受限账号，点得进去看是谁（链到上游用户列表 +
 *      status 筛选，不新造一张表）；
 *   ② 可达面 —— 受限账号仍能到达的接口分档，**只读**，由后端从会话白名单派生；
 *   ③ 公告 —— 受限账号登录后看到的那段话，从「内容管理」搬过来。
 *
 * 刻意**没有**收进来的（判断与理由写在这里，而不是散在 commit message 里）：
 *
 *   · **白名单本身不做成配置。** 它是 `middleware/restricted_user.go` 里的显式
 *     清单。做成可配的那一刻，「今后新增的接口默认对受限账号开放，而且没有人
 *     会注意到」这个失败模式就回来了 —— 那正是当初选白名单而不是黑名单的全部
 *     理由。这一页只展示它，没有任何写入口。
 *
 *   · **「受限账号能不能提工单」不做成开关。** 受限态的全部设计前提就是"能登录
 *     进来说话、不能动钱"；关掉工单之后，这个账号能登录、能看见自己被限制了、
 *     而没有任何出口 —— 比直接不让登录更糟，用户会反复登录、反复找，最后走站外
 *     渠道。它还是一个**全站**开关去治一个**单人**问题（某个受限账号刷单），
 *     而真要治那一个人，手段是关他的号，不是关所有人的申诉通道。
 *
 *   · **「受限账号的工单额度另算」也不收。** 这一条与上一条不同，它在比例上是
 *     成立的（限量不等于断路）。不收的理由是证据：站点当前 8 个受限账号一共开了
 *     1 张工单（还开着），而现有闸门是 `max_open_per_user=5` + 每日上限 +
 *     冷却 + 附件磁盘闸，且对受限账号与正常账号一视同仁地生效。在没有一条实测
 *     数据说明现有闸不够之前，加第二套阈值只是把一个未验证的猜想固化成配置项，
 *     代价是"同一个概念的第二份拷贝"和一个用户看不见却挡住他的数字。
 *     将来真要加，正确形状是**下限 ≥ 1** 的独立额度，而不是布尔开关。
 */
export function QyAdminRestrictedAccounts() {
  const { t } = useTranslation()
  const query = useQuery(qyAdminRestrictedAccountsQuery())

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_restricted_accounts')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button
          size='sm'
          variant='outline'
          render={
            // 不新造一张受限账号表：上游用户列表已经能按 status 筛，
            // 而那一页还带着改状态、改分组、看余额的全部动作。再造一张
            // 只读表的结果是运营在两张表之间来回跳，且两张表的口径迟早分叉。
            <Link to='/users' search={{ status: [DISABLED_STATUS_PARAM] }} />
          }
        >
          <Users className='size-4' />
          {t('qy_ra_view_users')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <div className='grid gap-4 lg:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)] lg:items-start'>
          <QyRestrictedNoticeCard />
          <div className='space-y-4'>
            {/*
              总览与公告是两条独立的取数：公告走扩展库（扩展库挂了它自己报错），
              总览只查主库。用一个 QyPageBoundary 把两者包在一起的话，扩展库
              一挂，连"现在有几个人被限制"都看不到了。
            */}
            <QyPageBoundary query={query}>
              {query.data != null && (
                <>
                  <OverviewCard count={query.data.count} />
                  <CapabilityCard capabilities={query.data.capabilities} />
                </>
              )}
            </QyPageBoundary>
          </div>
        </div>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}

/** 当前受限账号数。零与非零给的是两句不同的话，而不是同一句话配一个 0。 */
function OverviewCard(props: { count: number }) {
  const { t } = useTranslation()
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('qy_ra_overview_title')}</CardTitle>
        <CardDescription>{t('qy_ra_overview_desc')}</CardDescription>
      </CardHeader>
      <CardContent>
        {props.count === 0 ? (
          <p className='text-muted-foreground text-sm'>
            {t('qy_ra_count_zero')}
          </p>
        ) : (
          <div className='flex items-baseline gap-2'>
            <span className='text-3xl font-semibold tabular-nums'>
              {props.count}
            </span>
            <span className='text-muted-foreground text-sm'>
              {t('qy_ra_count_unit')}
            </span>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

/**
 * 受限账号仍然能做什么。**只读。**
 *
 * 分档与"哪几条路由属于哪一档"都由后端从白名单派生（见
 * `qianye/controller/restricted_accounts.go`）。前端只负责把 key 翻成人话，
 * 并把 `available=false` 的那一档如实标成"模块没开、这条通道实际不可达"。
 */
function CapabilityCard(props: {
  capabilities: { key: string; available: boolean; routes: string[] }[]
}) {
  const { t } = useTranslation()
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('qy_ra_cap_title')}</CardTitle>
        <CardDescription>{t('qy_ra_cap_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-4'>
        <ul className='space-y-3'>
          {props.capabilities.map((cap) => (
            <li key={cap.key} className='space-y-1'>
              <div className='flex items-center gap-2'>
                {cap.available ? (
                  <Check className='text-muted-foreground size-4 shrink-0' />
                ) : (
                  <Ban className='text-destructive size-4 shrink-0' />
                )}
                <span className='text-sm'>
                  {/* 后端加了一档而前端还没有文案时，退回 key 本身而不是渲染
                      一个空行 —— 空行会让这份清单看起来"就这么多"。 */}
                  {t(`qy_ra_cap_${cap.key}`, { defaultValue: cap.key })}
                </span>
                {!cap.available && (
                  <Badge variant='warning'>{t('qy_ra_cap_module_off')}</Badge>
                )}
              </div>
              <p className='text-muted-foreground pl-6 font-mono text-xs break-all'>
                {cap.routes.join(' · ')}
              </p>
            </li>
          ))}
        </ul>
        <p className='text-muted-foreground border-t pt-3 text-xs'>
          {t('qy_ra_cap_not_configurable')}
        </p>
      </CardContent>
    </Card>
  )
}
