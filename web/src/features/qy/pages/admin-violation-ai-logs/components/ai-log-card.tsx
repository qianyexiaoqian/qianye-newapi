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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import {
  qyGroupOptionLabel,
  qyGroupOptionsQuery,
} from '../../../lib/group-options'
import {
  qyAiChannelsQuery,
  qyAiLogDetailQuery,
  qyAiLogsQuery,
} from '../../admin-violation-ai-review/api'
import { QyPager } from '../../components/qy-pager'
import { formatQyMs, formatQyTs, QY_EMPTY_TEXT } from '../../ops/format'
import { QyFilterBar, QyFilterField } from '../../ops/qy-ops-ui'

const PAGE_SIZE = 20
const ALL = '__all__'

/**
 * 结局筛选的取值,与后端 `Outcome` 常量逐字对应。
 *
 * 漏一个的后果不是"少一个选项",而是那一类调用**筛不出来** —— 而失败的四类
 * (timeout / bad_json / upstream_error / no_channel)恰恰是排障时唯一要看的:
 * 审核失败一律放行,所以它们在用户侧、在 relay 侧都完全无感。
 */
const OUTCOMES = [
  'clean',
  'violation',
  'timeout',
  'bad_json',
  'upstream_error',
  'no_channel',
] as const

/**
 * AI 审核日志。
 *
 * ## 它回答四个问题
 *
 * 「哪个分组的哪个模型、转发前还是转发后、被谁触发、送审的是什么内容」——
 * 前三个是列,第四个是**内容**。内容不在列表里(一个 text 列,一页几十 KB,
 * 而表格里也放不下一段千字文本),点开某一行才去详情接口取。
 *
 * ## 内容是「模型读到的那一段」,不是用户原文
 *
 * 送审文本先经「送审内容上限」头尾截断,留存又在此基础上再截一次。存原文会让
 * 日志与判定依据对不上 —— 一条判"未违规"的记录旁边放着一段明显违规的原文,
 * 而真相是那一段根本没被送出去。它同时是**脱敏后**的:手机号、邮箱、密钥
 * 在入库前就被替换,库里从不存在未脱敏的原文。
 *
 * ## 这张表是滚动的
 *
 * 默认只保留 3 天(在上面的设置卡里改)。"查不到"不等于"没发生过" ——
 * 详情接口的 404 文案专门说的是"已超过保留期"。
 */
export function QyAiLogCard() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [group, setGroup] = useState(ALL)
  const [model, setModel] = useState('')
  const [phase, setPhase] = useState(ALL)
  const [outcome, setOutcome] = useState(ALL)
  const [violated, setViolated] = useState(ALL)
  const [userId, setUserId] = useState('')
  const [channelId, setChannelId] = useState(ALL)
  const [detailId, setDetailId] = useState<number | null>(null)

  const groups = useQuery(qyGroupOptionsQuery())
  // 渠道清单只为了把筛选项画成下拉。挂了两个以上渠道时,"哪个渠道在误判 /
  // 哪个渠道一直超时"是这一页最常被问的问题之一。
  const channels = useQuery(qyAiChannelsQuery())

  const query = useQuery(
    qyAiLogsQuery({
      p: page,
      page_size: PAGE_SIZE,
      group: group === ALL ? undefined : group,
      model: model.trim() || undefined,
      phase: phase === ALL ? undefined : phase,
      outcome: outcome === ALL ? undefined : outcome,
      violated: violated === ALL ? undefined : violated,
      user_id: userId.trim() === '' ? undefined : Number(userId),
      // 按 id 而不是名字:渠道可以改名,而历史明细里冗余的是改名前那一份。
      channel_id: channelId === ALL ? undefined : Number(channelId),
    })
  )

  const rows = query.data?.items ?? []

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('qy_ai_log_card_title')}</CardTitle>
        <CardDescription>{t('qy_ai_log_card_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='flex flex-col gap-3'>
        <QyFilterBar>
          <QyFilterField label={t('qy_ai_log_f_group')}>
            <Select
              value={group}
              onValueChange={(v) => {
                setGroup(v ?? ALL)
                setPage(1)
              }}
            >
              <SelectTrigger className='w-40'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
                {(groups.data?.options ?? []).map((g) => (
                  <SelectItem key={g.name} value={g.name}>
                    {qyGroupOptionLabel(g, groups.data?.probe_ok === true, t)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </QyFilterField>

          {/* 模型是自由文本而不是下拉:可选模型上千个,而后端做的是**精确**
              匹配 —— 模糊匹配在几百万行上会退化成全表扫,而且"这个模型被审了
              多少次"数出来会变成一批模型的合计。 */}
          <QyFilterField label={t('qy_ai_log_f_model')}>
            <Input
              className='w-52'
              value={model}
              placeholder={t('qy_ai_log_f_model_ph')}
              onChange={(e) => {
                setModel(e.target.value)
                setPage(1)
              }}
            />
          </QyFilterField>

          <QyFilterField label={t('qy_ai_log_f_phase')}>
            <Select
              value={phase}
              onValueChange={(v) => {
                setPhase(v ?? ALL)
                setPage(1)
              }}
            >
              <SelectTrigger className='w-44'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
                <SelectItem value='prompt'>
                  {t('qy_ai_log_phase_prompt')}
                </SelectItem>
                <SelectItem value='post_async'>
                  {t('qy_ai_log_phase_post_async')}
                </SelectItem>
              </SelectContent>
            </Select>
          </QyFilterField>

          <QyFilterField label={t('qy_ai_col_outcome')}>
            <Select
              value={outcome}
              onValueChange={(v) => {
                setOutcome(v ?? ALL)
                setPage(1)
              }}
            >
              <SelectTrigger className='w-44'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
                {OUTCOMES.map((o) => (
                  <SelectItem key={o} value={o}>
                    {t(`qy_ai_outcome_${o}` as never)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </QyFilterField>

          <QyFilterField label={t('qy_ai_log_f_violated')}>
            <Select
              value={violated}
              onValueChange={(v) => {
                setViolated(v ?? ALL)
                setPage(1)
              }}
            >
              <SelectTrigger className='w-32'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
                <SelectItem value='1'>{t('qy_ai_log_violated_yes')}</SelectItem>
                <SelectItem value='0'>{t('qy_ai_log_violated_no')}</SelectItem>
              </SelectContent>
            </Select>
          </QyFilterField>

          <QyFilterField label={t('qy_ai_log_f_channel')}>
            <Select
              value={channelId}
              onValueChange={(v) => {
                setChannelId(v ?? ALL)
                setPage(1)
              }}
            >
              <SelectTrigger className='w-44'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
                {(channels.data?.items ?? []).map((ch) => (
                  <SelectItem key={ch.id} value={String(ch.id)}>
                    {ch.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </QyFilterField>

          <QyFilterField label={t('qy_vio_filter_user_id')}>
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
        </QyFilterBar>

        <QyPageBoundary query={query}>
          <div className='overflow-x-auto'>
            <table className='w-full text-sm'>
              <thead>
                <tr className='text-muted-foreground text-left'>
                  <th className='py-1'>{t('qy_ai_log_col_time')}</th>
                  <th className='py-1'>{t('qy_ai_log_col_user')}</th>
                  <th className='py-1'>{t('qy_ai_log_f_group')}</th>
                  <th className='py-1'>{t('qy_ai_log_f_model')}</th>
                  <th className='py-1'>{t('qy_ai_log_f_phase')}</th>
                  <th className='py-1'>{t('qy_ai_log_col_reviewer')}</th>
                  <th className='py-1'>{t('qy_ai_col_outcome')}</th>
                  <th className='py-1'>{t('qy_ai_log_col_latency')}</th>
                  <th className='py-1'>{t('qy_ai_log_col_category')}</th>
                  <th className='py-1'>{t('qy_ai_log_col_content')}</th>
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
                      <span className='text-muted-foreground'>
                        {' '}
                        #{row.user_id}
                      </span>
                    </td>
                    <td className='py-1'>{row.using_group || QY_EMPTY_TEXT}</td>
                    <td className='py-1'>{row.model_name || QY_EMPTY_TEXT}</td>
                    <td className='py-1 whitespace-nowrap'>
                      {row.phase === 'post_async'
                        ? t('qy_ai_log_phase_post_async')
                        : t('qy_ai_log_phase_prompt')}
                    </td>
                    <td className='py-1'>
                      {/* 送到**哪个渠道、哪个模型**判的。它一直在行上,只是以前
                          只有点开详情才看得见 —— 而多渠道站点排障的第一刀就是
                          "是不是某一个渠道在误判"。渠道名下面压一行审核模型:
                          同一个渠道换过模型时,两者缺一都对不上号。 */}
                      <div className='whitespace-nowrap'>
                        {row.channel_name || QY_EMPTY_TEXT}
                      </div>
                      <div className='text-muted-foreground text-xs'>
                        {row.review_model || QY_EMPTY_TEXT}
                      </div>
                    </td>
                    <td className='py-1'>
                      {/* 判了违规的行用 destructive:结局列上 violation 与
                          clean 只差几个字母,而这两行在研判时的分量完全不同。 */}
                      <Badge variant={row.violated ? 'destructive' : 'outline'}>
                        {t(`qy_ai_outcome_${row.outcome}` as never)}
                      </Badge>
                    </td>
                    <td className='py-1 whitespace-nowrap'>
                      {/* 审核耗时。**转发前**那一档它是实打实加在用户首字节延迟上的,
                          转发后那一档不占用户的时间 —— 同一个数字在两档里的分量
                          完全不同,所以时机列就在左边几格,两者要并排看。

                          attempts > 1 时把次数缀上:一次审核最多打三个渠道
                          (故障转移),3 秒可能是"这一次真的慢",也可能是
                          "前两个渠道各挂了一次"。没有分母的耗时读不出是哪一种。 */}
                      {formatQyMs(row.latency_ms)}
                      {row.attempts > 1 ? (
                        <span className='text-muted-foreground text-xs'>
                          {' · '}
                          {t('qy_ai_log_attempts', { count: row.attempts })}
                        </span>
                      ) : null}
                    </td>
                    <td className='py-1'>{row.category || QY_EMPTY_TEXT}</td>
                    <td className='py-1'>
                      {row.content_chars > 0 ? (
                        <Button
                          size='sm'
                          variant='outline'
                          onClick={() => setDetailId(row.id)}
                        >
                          {t('qy_ai_log_view_content', {
                            chars: row.content_chars,
                          })}
                        </Button>
                      ) : (
                        <span className='text-muted-foreground text-xs'>
                          {t('qy_ai_log_no_content')}
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

      <AiLogContentDialog id={detailId} onClose={() => setDetailId(null)} />
    </Card>
  )
}

/**
 * 送审内容详情。
 *
 * 只在打开时才发请求(`enabled: id != null`):内容是这一页唯一一段用户原文,
 * 没有理由在列表渲染时就把整页的内容全拉下来 —— 那既是流量,也是一次
 * "谁在批量读用户内容"说不清楚的访问。
 */
function AiLogContentDialog(props: { id: number | null; onClose: () => void }) {
  const { t } = useTranslation()
  const query = useQuery({
    ...qyAiLogDetailQuery(props.id ?? 0),
    enabled: props.id != null,
  })
  const data = query.data

  return (
    <QyResponsiveDialog
      open={props.id != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_ai_log_content_title')}
      description={t('qy_ai_log_content_desc')}
    >
      <div className='flex flex-col gap-3'>
        {data ? (
          <>
            <div className='text-muted-foreground grid gap-1 text-xs sm:grid-cols-2'>
              <span>
                {t('qy_ai_log_f_group')}:{' '}
                {data.item.using_group || QY_EMPTY_TEXT}
              </span>
              <span>
                {t('qy_ai_log_f_model')}:{' '}
                {data.item.model_name || QY_EMPTY_TEXT}
              </span>
              <span>
                {t('qy_ai_log_col_user')}: {data.item.username || QY_EMPTY_TEXT}{' '}
                #{data.item.user_id}
              </span>
              <span>
                {t('qy_ai_log_col_reviewer')}:{' '}
                {data.item.channel_name || QY_EMPTY_TEXT} /{' '}
                {data.item.review_model || QY_EMPTY_TEXT}
              </span>
              <span>
                {t('qy_ai_log_col_latency')}: {formatQyMs(data.item.latency_ms)}
                {data.item.attempts > 1
                  ? ` · ${t('qy_ai_log_attempts', { count: data.item.attempts })}`
                  : ''}
              </span>
            </div>

            {/* 模型给的理由与内容摆在一起:分开看的话,"它凭什么这么判"要在
                两个地方来回翻,而那正是这一页存在的理由。 */}
            {data.item.reason ? (
              <Alert>
                <AlertTitle>{t('qy_ai_log_reason')}</AlertTitle>
                <AlertDescription>{data.item.reason}</AlertDescription>
              </Alert>
            ) : null}

            {data.item.content ? (
              <>
                <pre className='bg-muted max-h-96 overflow-auto rounded p-2 text-xs whitespace-pre-wrap'>
                  {data.item.content}
                </pre>
                <p className='text-muted-foreground text-xs'>
                  {t('qy_ai_log_content_redacted')}
                </p>
                {data.truncated ? (
                  <p className='text-muted-foreground text-xs'>
                    {t('qy_ai_log_content_truncated', {
                      chars: data.item.content_chars,
                    })}
                  </p>
                ) : null}
              </>
            ) : (
              /* 空内容有两种成因,而运营最常问的正是"为什么这条没有内容"。
                 给出当前开关,至少能把"整个功能没开"这一种当场排除掉。 */
              <Alert>
                <AlertTitle>{t('qy_ai_log_content_empty_title')}</AlertTitle>
                <AlertDescription>
                  {data.log_content_enabled
                    ? t('qy_ai_log_content_empty_on')
                    : t('qy_ai_log_content_empty_off')}
                </AlertDescription>
              </Alert>
            )}
          </>
        ) : (
          <p className='text-muted-foreground text-sm'>
            {query.isError
              ? qyErrorMessage(query.error, t)
              : t('qy_ai_log_content_loading')}
          </p>
        )}
      </div>
    </QyResponsiveDialog>
  )
}
