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
import { Plus, Ticket } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { qyArray } from '../../../lib/array'
import { QyPager } from '../../components/qy-pager'
import { QY_LOT_PAGE_SIZE, qyLotActivitiesQuery } from '../api'
import type { QyLotHallLane, QyLotHallPhase } from '../api'
import { useQyNowSeconds } from '../lib/use-now'
import { QyLotActivityCard } from './lottery-activity-card'

/**
 * 大厅列表正文（抽奖 / 竞猜 / 双色球共用）。
 *
 * ## 为什么 `lane` 变成了参数而不是一个下拉框
 *
 * 合并成选择夹之后，玩法从"同一张列表里的一个筛选值"升成"一张标签"。这不是
 * 换个位置：抽奖与竞猜的**资金语义不同** —— 抽奖是参与费不退、赢多少由奖档
 * 决定；竞猜是奖池再分配、可能亏掉本金。两者混在同一个列表里靠一个下拉框
 * 区分，用户按上一次的预期下注就会亏，而界面上没有任何东西提醒他换了规则。
 *
 * 双色球本轮从抽奖那一夹里再拆出来（项目方原话：「把双色球和竞猜分开选择夹，
 * 抽奖-竞猜-双色球」）。它与按名次/按公示概率的资金语义相同（参与费不退），
 * 拆的理由是**用户在它上面要做的事不同**：选号、看开奖号、对红蓝球。混在
 * 同一张列表里时，想买一注双色球的人得先翻过若干场跟他无关的抽奖。
 *
 * ## 分夹为什么必须由后端做
 *
 * 前端拿到一页再过滤，会让「双色球」那一页只剩零星几条（整页被滤掉的不会
 * 补上），分页总数也是错的。所以 `lane` 一路发到 SQL 的 WHERE 里。
 *
 * ## 「进行中 / 已结束」为什么留在标签内部（两级而不是并列）
 *
 * 上一级标签分的是**玩法**（我今天想玩哪一种），这一级分的是**同一批对象的
 * 状态**（还能不能参加）。两者不是同一维度：拍平成并列的六张标签
 * （抽奖·进行中 / 抽奖·已结束 / 竞猜·进行中 / …）会让标签栏随玩法数量翻倍，
 * 用户每次都要在六张里找自己那一张。
 *
 * 也没有做成"顶部标签 + 一个状态下拉筛选"：进行中与已结束是这一页上切换得
 * 最频繁的一件事（看完在跑的，回头翻历史复核），下拉框要两次点击，而且当前
 * 在哪一档要展开才看得见。分段栏一次点击、状态一眼可见。
 *
 * 已结束那一档是「历史公正查询」的入口 —— 它不是归档，是每个人随时可以回去
 * 复核的地方。
 *
 * ## 分段与页码为什么是受控的（由宿主持有）
 *
 * `QyPageTabs` 刻意不加 `keepMounted`，Base UI 的面板隐藏即卸载，本组件自己
 * 的 `useState` 会在切走标签时归零。表现是：用户翻到「已结束」第 3 页做历史
 * 公正查询，切去「我的参与」核对一条记录再切回来，那一屏没了，要重新翻三次。
 * 状态提到宿主（它不随标签卸载）之后，切换标签只是换一张面板，位置留在原处。
 */
/**
 * 每张选择夹空态那句话的 i18n 键。
 *
 * 写成 `Record<QyLotHallLane, string>` 而不是三元链：类型系统会在新增一张夹时
 * 当场要求补上这一行，而三元链的缺省分支会让新夹静默复用抽奖那句话 —— 一句
 * 讲错玩法的说明比没有说明更糟。
 */
const QY_LOT_EMPTY_DESC_KEY: Record<QyLotHallLane, string> = {
  ball: 'qy_lot_empty_ball_desc',
  draw: 'qy_lot_empty_draw_desc',
  guess: 'qy_lot_empty_guess_desc',
}

export type QyLotHallState = {
  onPageChange: (page: number) => void
  onScopeChange: (scope: QyLotHallPhase) => void
  page: number
  scope: QyLotHallPhase
}

export function QyLotHallList(props: QyLotHallState & { lane: QyLotHallLane }) {
  const { t } = useTranslation()
  const now = useQyNowSeconds()
  const { page, scope } = props
  /*
    ── 空态要分两种人看（需求 6）──

    项目方原话：「抽奖竞猜页面，没有发现"双色球"活动 UI 界面和配置活动界面。」
    实测下来站里 `qy_lot_activity` 一条记录都没有，于是大厅永远是一句
    「暂无活动」——**管理员看到的和普通用户看到的一模一样**，没有任何东西告诉他
    "活动是在管理端建的，入口在那边"。功能全都写完了，看起来却像没做。

    所以空态按角色分叉：普通用户是一句"等新活动"，管理员额外拿到一颗直达
    创建入口的按钮。判据用 `role >= ADMIN`（与那个路由自己的守卫同一条），
    对普通用户渲染这颗按钮只会把他送去一个 403。
  */
  const isAdmin =
    (useAuthStore((state) => state.auth.user?.role) ?? ROLE.GUEST) >= ROLE.ADMIN

  /*
    分区参数走 `phase`，取值 `live` / `ended` —— 与后端 `hallPhases` 的键逐字
    一致。这里曾经发的是 `status=open|done`，后端读的却是 `phase`，两档分段
    因此拿回同一份列表；口径只留一套名字就没有第二处可以漂移。
  */
  const params = {
    p: page,
    page_size: QY_LOT_PAGE_SIZE,
    lane: props.lane,
    phase: scope,
  }
  const query = useQuery(qyLotActivitiesQuery(params))
  /*
    草稿在这里**再挡一次**。说了算的仍是后端那句 `WHERE status <> 'draft'`
    （见 `qianye/modules/lottery/api_user.go` 的 hallQuery），这一行是第二道：
    草稿是一份还没有承诺、随时可能被改掉的规则，让用户看见它一眼就是一次
    真实的泄漏，而它可以由任何一次列表查询的重构悄悄造成 —— 上一版那个没有
    default 分支的 phase switch 正是这类"改一处、静默换掉整份结果集"的先例。
    代价只是这一行；漏出去的代价不是。
  */
  const items = qyArray(query.data?.items).filter(
    (activity) => activity.status !== 'draft'
  )

  return (
    <div className='space-y-3'>
      {/*
        分段栏与风险徽章同一行。徽章此前是各张标签顶上的一整块带边框横幅
        （「参与费不退」五个字、「猜错会亏本金」六个字），把首屏最值钱的那条位置
        用掉了 —— 而用户打开大厅要看的是奖池、参与费、还剩多久、多少人参加。
        文案一字未改，只是从横幅缩成徽章，位置仍然挂在标签上而不是卡片上，
        因此照样不会被翻页翻掉。
      */}
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <Tabs
          value={scope}
          onValueChange={(value) => {
            props.onScopeChange(value === 'ended' ? 'ended' : 'live')
            props.onPageChange(1)
          }}
        >
          <TabsList>
            <TabsTrigger value='live'>{t('qy_lot_tab_open')}</TabsTrigger>
            <TabsTrigger value='ended'>{t('qy_lot_tab_done')}</TabsTrigger>
          </TabsList>
        </Tabs>
        {/*
          风险徽章按**资金语义**分，不按标签分：双色球与按名次/按公示概率
          一样是"参与费不退"，只有竞猜会亏本金。三张标签两种徽章，这里刻意
          不给双色球编第三句话 —— 它的最坏结果与抽奖一模一样，另写一句只会
          让用户以为那是第三种规则。
        */}
        <Badge variant={props.lane === 'guess' ? 'destructive' : 'outline'}>
          {props.lane === 'guess'
            ? t('qy_lot_risk_badge_may_lose_principal')
            : t('qy_lot_risk_badge_stake_lost')}
        </Badge>
      </div>

      {/* 列表刻意留在 `Tabs` 外面：两档分段的内容形状完全一样，差别只在
          请求参数。写成两个 `TabsContent` 就是同一段渲染的第二份拷贝，
          迟早漂移成"已结束那张忘了显示结局"。 */}
      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyIcon={Ticket}
        /*
          「进行中一个都没有」与「还没有任何已结束的」是两回事：前者可能只是这
          一阵没开新场，后者说明这个玩法从来没跑过一次。写同一句话，用户会以为
          自己点错了标签。
        */
        emptyTitle={
          scope === 'live'
            ? t('qy_lot_empty_open_title')
            : t('qy_lot_empty_done_title')
        }
        /*
          三张夹各说各的：抽奖是按奖档发奖、竞猜是奖池再分配、双色球是自己
          选号按命中个数定档。空态是这一屏唯一还在说话的地方，顺手把"这里
          将来会出现什么"说清楚 —— 一句「暂无活动」等于让用户自己去猜这个
          站到底有没有这个玩法。

          双色球此前被塞在抽奖那一句里点名（"双色球也在这一栏"）。它自己有
          标签之后那半句必须删掉：留着就是指着一张空列表说"它在隔壁"。
        */
        emptyDescription={t(QY_LOT_EMPTY_DESC_KEY[props.lane])}
        /*
          「进行中」空掉是常态而不是异常：一批活动结束、下一批还没发布的间隙里，
          默认视图对每个人都是空的。此时把人原地晾着，他会以为整个玩法坏了 ——
          实测库里就是 0 条进行中、64 条已结束。所以这一档给一条通往历史的出口，
          管理员则给创建入口（他要的是"去开一场新的"，不是"去翻旧的"）。
        */
        emptyAction={
          isAdmin ? (
            <Button
              size='sm'
              render={<Link to='/qy/admin/lottery' />}
              aria-label={t('qy_lot_empty_admin_create')}
            >
              <Plus aria-hidden='true' />
              {t('qy_lot_empty_admin_create')}
            </Button>
          ) : scope === 'live' ? (
            <Button
              size='sm'
              variant='outline'
              onClick={() => {
                props.onScopeChange('ended')
                props.onPageChange(1)
              }}
            >
              {t('qy_lot_empty_open_see_done')}
            </Button>
          ) : undefined
        }
      >
        <div className='space-y-3'>
          <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
            {items.map((activity) => (
              <QyLotActivityCard
                key={activity.act_no}
                activity={activity}
                nowSeconds={now}
              />
            ))}
          </div>
          <QyPager
            page={page}
            pageSize={QY_LOT_PAGE_SIZE}
            total={query.data?.total ?? 0}
            onPageChange={props.onPageChange}
            disabled={query.isFetching}
          />
        </div>
      </QyPageBoundary>
    </div>
  )
}
