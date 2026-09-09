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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyKeyValue } from '../ops/qy-ops-ui'
import { qyRiskWatchStatsQuery } from './api'
import { QyRwCaptureCard } from './components/capture-card'
import { QyRwTaskCard } from './components/task-card'

/**
 * 风控预警 —— 管理员对可疑账号的定向监听取证。
 *
 * ## 它与「违规记录」「AI 审核日志」不是一件事
 *
 * 那两页回答的是「这一条内容违不违规」:判据是规则,处置是拦截、扣费、封号,
 * 而且是**全站自动**跑的。这一页回答「这个账号最近到底在做什么」——
 * 由管理员指名立案、只观察不处置。
 *
 * 一个刚被举报的账号,规则库里没有任何一条能命中他,而"他到底在问什么"
 * 在这一页之前没有任何地方查得到。查到之后的处置仍然走既有的路
 * (违规规则、封号策略、工单),这一页只负责让人看得到该处置谁。
 *
 * ## 它需要一个单独配置的存储节点
 *
 * 记录存的是被抽中请求的完整上下文,而抽多少、抽多久由管理员在这一页上临时
 * 决定 —— 一个「永久监听 + 100% 概率」的任务一天就能写进几十 GB。所以它强制
 * 要求 `risk_watch.database.dsn`(见 `qianye/config/qianye.example.yaml`),
 * 没配时整个功能不注册,连这一页的入口都不渲染。
 */
export function QyAdminRiskWatch() {
  const { t } = useTranslation()
  // 任务卡上点「查看记录」时把它写下来,记录卡据此筛选。提到父组件而不是让
  // 记录卡自己存:两张卡之间只有这一个联动,而它正是这一页的主要动线
  // (先看有哪些任务在跑,再看某一个抓到了什么)。
  const [taskId, setTaskId] = useState(0)
  const stats = useQuery(qyRiskWatchStatsQuery())

  const store = stats.data?.store
  // 存储节点挂了的表现是"列表一直是空的",而空列表与"这个用户很干净"长得
  // 一模一样。这条横幅是页面上唯一能分辨这两件事的地方。
  const storeDown = store != null && store.configured && store.available === false

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_risk_watch')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <div className='flex flex-col gap-4'>
          {storeDown ? (
            <Alert variant='destructive'>
              <AlertTitle>{t('qy_rw_store_down_title')}</AlertTitle>
              <AlertDescription>{t('qy_rw_store_down_desc')}</AlertDescription>
            </Alert>
          ) : null}

          {stats.data ? (
            <div className='grid gap-x-6 sm:grid-cols-2 lg:grid-cols-4'>
              <QyKeyValue label={t('qy_rw_stat_running')}>
                {stats.data.running}
              </QyKeyValue>
              <QyKeyValue label={t('qy_rw_stat_tasks')}>
                {stats.data.tasks}
              </QyKeyValue>
              <QyKeyValue label={t('qy_rw_stat_captures')}>
                {stats.data.captures}
              </QyKeyValue>
              {/* 单条记录的字符上限。它在 YAML 里(risk_watch.capture_max_chars),
                  改不了但必须看得见:一条被截断的记录读起来完全通顺,而"我看到的
                  就是全部"是研判时最危险的默认假设。 */}
              <QyKeyValue label={t('qy_rw_stat_max_chars')}>
                {stats.data.capture_max_chars}
              </QyKeyValue>
            </div>
          ) : null}

          <QyRwTaskCard onInspect={setTaskId} />
          <QyRwCaptureCard taskId={taskId} onTaskIdChange={setTaskId} />
        </div>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
