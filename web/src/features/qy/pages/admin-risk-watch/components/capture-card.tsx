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

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QyPager } from '../../components/qy-pager'
import { formatQyTs, QY_EMPTY_TEXT } from '../../ops/format'
import { QyFilterBar, QyFilterField } from '../../ops/qy-ops-ui'
import { qyRiskWatchCaptureQuery, qyRiskWatchCapturesQuery } from '../api'

const PAGE_SIZE = 20

/**
 * 监听记录列表。
 *
 * ## 正文不在列表里
 *
 * 一行带着最多几千字的上下文,一页 20 条就是一次几百 KB 的响应,而表格里也
 * 放不下一段千字文本。更要紧的是它同时是一道很自然的边界:**翻列表不等于
 * 读了每一条的内容** —— 点开哪一条才去取哪一条。
 *
 * ## 抓的是转发**之前**那一刻
 *
 * 记录里只有用户发过去的上下文,没有模型的回复。回复要在转发之后才存在,
 * 而流式响应是边转发边吐的,要留档就得在热路径上边转发边聚合整段输出。
 * 这条边界写在后端 `qianye/modules/riskwatch/model.go` 的包注释里。
 *
 * ## 这张表是滚动的
 *
 * 每个任务各有各的保留期,过期即清。"查不到"不等于"没发生过"。
 */
export function QyRwCaptureCard(props: {
  /** 从任务列表点「查看记录」带过来的任务 id;0 表示不筛。 */
  taskId: number
  onTaskIdChange: (taskId: number) => void
}) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [userId, setUserId] = useState('')
  const [model, setModel] = useState('')
  const [detailId, setDetailId] = useState<number | null>(null)

  const query = useQuery(
    qyRiskWatchCapturesQuery({
      p: page,
      page_size: PAGE_SIZE,
      task_id: props.taskId > 0 ? props.taskId : undefined,
      user_id: userId.trim() === '' ? undefined : Number(userId),
      model_name: model.trim() || undefined,
    })
  )
  const rows = query.data?.items ?? []

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('qy_rw_capture_card_title')}</CardTitle>
        <CardDescription>{t('qy_rw_capture_card_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='flex flex-col gap-3'>
        <QyFilterBar>
          <QyFilterField label={t('qy_rw_f_task')}>
            <Input
              className='w-28'
              inputMode='numeric'
              value={props.taskId > 0 ? String(props.taskId) : ''}
              placeholder={t('qy_common_all')}
              onChange={(e) => {
                props.onTaskIdChange(
                  Number.parseInt(e.target.value.replaceAll(/\D/gu, ''), 10) || 0
                )
                setPage(1)
              }}
            />
          </QyFilterField>
          <QyFilterField label={t('qy_rw_col_user')}>
            <Input
              className='w-28'
              inputMode='numeric'
              value={userId}
              onChange={(e) => {
                setUserId(e.target.value.replaceAll(/\D/gu, ''))
                setPage(1)
              }}
            />
          </QyFilterField>
          {/* 模型是自由文本而不是下拉:可选模型上千个,而后端做的是**精确**
              匹配 —— 模糊匹配在几百万行上会退化成全表扫。 */}
          <QyFilterField label={t('qy_rw_col_model')}>
            <Input
              className='w-52'
              value={model}
              onChange={(e) => {
                setModel(e.target.value)
                setPage(1)
              }}
            />
          </QyFilterField>
        </QyFilterBar>

        <QyPageBoundary query={query}>
          <div className='overflow-x-auto'>
            <table className='w-full text-sm'>
              <thead>
                <tr className='text-muted-foreground text-left'>
                  <th className='py-1'>{t('qy_rw_col_time')}</th>
                  <th className='py-1'>{t('qy_rw_col_user')}</th>
                  <th className='py-1'>{t('qy_rw_col_token')}</th>
                  <th className='py-1'>{t('qy_rw_col_group')}</th>
                  <th className='py-1'>{t('qy_rw_col_model')}</th>
                  <th className='py-1'>{t('qy_rw_col_ip')}</th>
                  <th className='py-1'>{t('qy_rw_col_expires')}</th>
                  <th className='py-1'>{t('qy_rw_col_content')}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.id} className='border-t align-top'>
                    <td className='py-1 whitespace-nowrap'>
                      {formatQyTs(row.created_at)}
                    </td>
                    <td className='py-1'>
                      {row.username || QY_EMPTY_TEXT}
                      <span className='text-muted-foreground'> #{row.user_id}</span>
                    </td>
                    <td className='py-1'>
                      {/* 哪一把密钥发的。同一个账号下的多把密钥常常对应不同的
                          用途(自己用的、卖出去的、挂在某个客户端里的),
                          而"哪一把在刷"决定了处置面。 */}
                      {row.token_name || QY_EMPTY_TEXT}
                    </td>
                    <td className='py-1'>{row.user_group || QY_EMPTY_TEXT}</td>
                    <td className='py-1'>
                      {row.model_name || QY_EMPTY_TEXT}
                      {row.is_stream ? (
                        <Badge variant='outline' className='ml-1'>
                          {t('qy_rw_stream')}
                        </Badge>
                      ) : null}
                    </td>
                    <td className='py-1'>{row.client_ip || QY_EMPTY_TEXT}</td>
                    <td className='py-1 whitespace-nowrap'>
                      {row.expires_at > 0
                        ? formatQyTs(row.expires_at)
                        : t('qy_rw_retention_forever')}
                    </td>
                    <td className='py-1'>
                      {row.content_chars > 0 ? (
                        <Button
                          size='sm'
                          variant='outline'
                          onClick={() => setDetailId(row.id)}
                        >
                          {t('qy_rw_view_content', { chars: row.content_chars })}
                        </Button>
                      ) : (
                        <span className='text-muted-foreground text-xs'>
                          {t('qy_rw_no_content')}
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <QyPager
            page={page}
            pageSize={PAGE_SIZE}
            total={query.data?.total ?? 0}
            onPageChange={setPage}
            disabled={query.isFetching}
          />
        </QyPageBoundary>
      </CardContent>

      <QyRwContentDialog id={detailId} onClose={() => setDetailId(null)} />
    </Card>
  )
}

/**
 * 一条记录的完整上下文。
 *
 * 只在打开时才发请求(`enabled: id != null`):正文是这一页唯一一段用户原文,
 * 没有理由在列表渲染时就把整页的内容全拉下来 —— 那既是流量,也是一次
 * "谁在批量读用户内容"说不清楚的访问。
 */
function QyRwContentDialog(props: { id: number | null; onClose: () => void }) {
  const { t } = useTranslation()
  const query = useQuery({
    ...qyRiskWatchCaptureQuery(props.id ?? 0),
    enabled: props.id != null,
  })
  const data = query.data

  return (
    <QyResponsiveDialog
      open={props.id != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_rw_content_title')}
      description={t('qy_rw_content_desc')}
    >
      <div className='flex flex-col gap-3'>
        {data ? (
          <>
            <div className='text-muted-foreground grid gap-1 text-xs sm:grid-cols-2'>
              <span>
                {t('qy_rw_col_time')}: {formatQyTs(data.created_at)}
              </span>
              <span>
                {t('qy_rw_col_model')}: {data.model_name || QY_EMPTY_TEXT}
              </span>
              <span>
                {t('qy_rw_col_group')}: {data.user_group || QY_EMPTY_TEXT}
              </span>
              <span>
                {t('qy_rw_col_token')}: {data.token_name || QY_EMPTY_TEXT}
              </span>
              <span>
                {t('qy_rw_col_request')}: {data.request_id || QY_EMPTY_TEXT}
              </span>
              <span>
                {t('qy_rw_col_ip')}: {data.client_ip || QY_EMPTY_TEXT}
              </span>
            </div>

            {/* 截断提示必须显式出现:一段被掐掉中间的文本读起来完全通顺,
                而"我看到的就是全部"是研判时最危险的一个默认假设。 */}
            {data.truncated ? (
              <p className='text-muted-foreground text-xs'>
                {t('qy_rw_content_truncated', { chars: data.content_chars })}
              </p>
            ) : null}

            <pre className='bg-muted max-h-96 overflow-auto rounded p-3 text-xs whitespace-pre-wrap'>
              {data.content || QY_EMPTY_TEXT}
            </pre>

            {/* 多模态附件只留描述符(MIME / 字节数 / SHA256),二进制本体一个
                字节都不入库。哈希留着是为了把"同一张图被多个账号反复上传"
                串起来,而完全不必保存图片本体。 */}
            {data.files ? (
              <div className='space-y-1'>
                <p className='text-xs font-medium'>{t('qy_rw_files')}</p>
                <pre className='bg-muted max-h-40 overflow-auto rounded p-3 text-xs whitespace-pre-wrap'>
                  {data.files}
                </pre>
              </div>
            ) : null}
          </>
        ) : (
          <p className='text-muted-foreground text-sm'>
            {query.isPending ? t('qy_rw_loading') : t('qy_rw_content_gone')}
          </p>
        )}
      </div>
    </QyResponsiveDialog>
  )
}
