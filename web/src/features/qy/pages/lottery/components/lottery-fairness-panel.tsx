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
import {
  CircleCheck,
  CircleMinus,
  CircleX,
  Download,
  Loader2,
  ShieldCheck,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

import { QyTimeline } from '../../../components/qy-timeline'
import type { QyTimelineItem } from '../../../lib/types'
import { QY_EMPTY_TEXT, formatQyTs } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { qyLotFullProofQuery, qyLotProofDownloadUrl } from '../api'
import { verifyQyLotProof, type QyLotVerifyStep } from '../lib/verify'
import type { QyLotActivityDetail, QyLotProof } from '../types'

const STEP_ICON = {
  ok: CircleCheck,
  fail: CircleX,
  skipped: CircleMinus,
} as const

const STEP_CLASS = {
  ok: 'text-success',
  fail: 'text-destructive',
  skipped: 'text-muted-foreground',
} as const

/**
 * 公正性面板。
 *
 * ## 它凭什么值得信
 *
 * 「下载数据自己去验」在实践中等于没人验。所以这里提供的是**浏览器内本地复算**：
 * 点一下按钮，WebCrypto 在用户自己的机器上把整套 `lot-v1` 重跑一遍，逐步显示
 * 每一步的结果。用户亲眼看到复算跑出同一份名单，比任何文字说明都有力。
 *
 * 复算过程**不请求任何服务端验证接口** —— 那会让"自己验"退化成"让平台说它验过
 * 了"，而后者一点公正性都没有。唯一的网络请求是把证据链拿下来。
 *
 * ## 时间顺序才是关键
 *
 * `roster_hash` 在**封盘那一刻**就已公开，而 `seed` 要等到开奖才公布。任何人在
 * 这两个时刻之间抓一份证据链，就持有了平台无法否认的名单快照。时间线把这个
 * 先后顺序摆出来，而不只是罗列几个哈希。
 */
export function QyLotFairnessPanel(props: { activity: QyLotActivityDetail }) {
  const { activity } = props
  const { t } = useTranslation()
  const [steps, setSteps] = useState<QyLotVerifyStep[] | null>(null)
  const [running, setRunning] = useState(false)

  // 证据链只在封盘之后才存在（设计文档 T2）。之前请求只会拿到 409。
  const ready =
    activity.status === 'locked' ||
    activity.status === 'settling' ||
    activity.status === 'finished'

  // 整份取回（内部自动翻页）。链与名单必须**整份**才验得了 —— 少一条链就断，
  // 而"断了"与"被篡改了"在结果上无法区分。要一个很大的 page_size 是行不通的：
  // 服务端对越界页长的口径是回落默认值,要得越多反而拿得越少。
  const query = useQuery(qyLotFullProofQuery(activity.act_no, ready))
  const proof = query.data

  const runVerify = async () => {
    if (proof == null) return
    setRunning(true)
    try {
      setSteps(await verifyQyLotProof(proof))
    } catch (error) {
      // WebCrypto 不可用（非安全上下文）等硬失败。**不能悄悄回落**到别的实现，
      // 那等于让用户以为验过了而实际上验的是另一套东西。
      setSteps([
        {
          key: 'rules',
          labelKey: 'qy_lot_vf_step_rules',
          status: 'fail',
          detail: String(error),
        },
      ])
    } finally {
      setRunning(false)
    }
  }

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle className='flex items-center gap-2'>
          <ShieldCheck aria-hidden='true' className='size-4' />
          {t('qy_lot_fairness_title')}
        </CardTitle>
        <CardDescription>{t('qy_lot_fairness_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-4'>
        {!ready ? (
          <p className='text-muted-foreground text-sm'>
            {t('qy_lot_fairness_not_ready')}
          </p>
        ) : (
          <>
            <QyTimeline items={buildTimeline(activity, proof, t)} />

            <div>
              <QyKeyValue label={t('qy_lot_commit_hash')}>
                <span className='font-mono text-xs'>
                  {activity.commit_hash === ''
                    ? QY_EMPTY_TEXT
                    : activity.commit_hash}
                </span>
              </QyKeyValue>
              <QyKeyValue label={t('qy_lot_roster_hash')}>
                <span className='font-mono text-xs'>
                  {proof?.roster_hash === '' || proof == null
                    ? QY_EMPTY_TEXT
                    : proof.roster_hash}
                </span>
              </QyKeyValue>
              <QyKeyValue label={t('qy_lot_roster_count')}>
                {proof?.roster_count ?? QY_EMPTY_TEXT}
              </QyKeyValue>
              <QyKeyValue label={t('qy_lot_seed')}>
                <span className='font-mono text-xs'>
                  {proof == null || proof.seed === ''
                    ? t('qy_lot_seed_sealed')
                    : proof.seed}
                </span>
              </QyKeyValue>
              <QyKeyValue label={t('qy_lot_chain_head')}>
                <span className='font-mono text-xs'>
                  {proof?.chain_head ?? QY_EMPTY_TEXT}
                </span>
              </QyKeyValue>
            </div>

            {/* 拿到的条目不完整时**必须说出来**，并且不给"验证通过"的假象。 */}
            {proof != null && proof.entries.length !== proof.total && (
              <Alert>
                <AlertTitle>{t('qy_lot_vf_partial_title')}</AlertTitle>
                <AlertDescription>
                  {t('qy_lot_vf_partial_desc', {
                    loaded: proof.entries.length,
                    total: proof.total,
                  })}
                </AlertDescription>
              </Alert>
            )}

            <div className='flex flex-wrap gap-2'>
              <Button
                type='button'
                size='sm'
                disabled={proof == null || running}
                onClick={() => {
                  void runVerify()
                }}
              >
                {running && (
                  <Loader2 aria-hidden='true' className='animate-spin' />
                )}
                {t('qy_lot_vf_run')}
              </Button>
              <Button
                type='button'
                size='sm'
                variant='outline'
                render={
                  <a
                    href={qyLotProofDownloadUrl(activity.act_no)}
                    download={`${activity.act_no}-proof.ndjson`}
                  />
                }
              >
                <Download aria-hidden='true' />
                {t('qy_lot_vf_download')}
              </Button>
            </div>

            {steps != null && (
              <ul className='space-y-1.5 text-sm'>
                {steps.map((step) => {
                  const Icon = STEP_ICON[step.status]
                  return (
                    <li key={step.key} className='flex items-start gap-2'>
                      <Icon
                        aria-hidden='true'
                        className={`mt-0.5 size-4 shrink-0 ${STEP_CLASS[step.status]}`}
                      />
                      <span className='min-w-0'>
                        <span className='block'>{t(step.labelKey)}</span>
                        {step.detail != null && (
                          // 技术细节刻意不翻译：失败时用户要能把它原样贴给别人。
                          <span className='text-muted-foreground block font-mono text-xs break-all'>
                            {step.detail}
                          </span>
                        )}
                      </span>
                    </li>
                  )
                })}
              </ul>
            )}

            <p className='text-muted-foreground text-xs'>
              {t('qy_lot_vf_local_note')}
            </p>
          </>
        )}
      </CardContent>
    </Card>
  )
}

/**
 * 承诺 → 冻结 → 揭示 → 结算。
 *
 * 未到达的节点保留灰色占位而不是不渲染：用户需要知道"后面还有几步"，
 * 尤其是"种子要到开奖才公布"这一步 —— 那正是整套协议成立的原因。
 */
function buildTimeline(
  activity: QyLotActivityDetail,
  proof: QyLotProof | undefined,
  t: (key: string) => string
): QyTimelineItem[] {
  const revealed = proof != null && proof.seed !== ''
  return [
    {
      key: 'commit',
      title: t('qy_lot_tl_commit'),
      description: t('qy_lot_tl_commit_desc'),
      timestamp: activity.open_at,
      state: activity.commit_hash === '' ? 'pending' : 'done',
    },
    {
      key: 'freeze',
      title: t('qy_lot_tl_freeze'),
      description: t('qy_lot_tl_freeze_desc'),
      // 显示**实际**封盘时刻而不是计划的 close_at：它与揭示时刻之间的那一段
      // 才是"任何人都能抓一份平台无法否认的名单快照"的窗口，而封盘任务落后时
      // 这两个值可以差很远。
      timestamp: proof?.locked_at ?? activity.close_at,
      state: proof != null && proof.roster_hash !== '' ? 'done' : 'pending',
    },
    {
      key: 'reveal',
      title: t('qy_lot_tl_reveal'),
      description: t('qy_lot_tl_reveal_desc'),
      timestamp: proof?.revealed_at ?? activity.draw_at,
      state: revealed ? 'done' : 'pending',
    },
    {
      key: 'settle',
      title: t('qy_lot_tl_settle'),
      description: formatQyTs(proof?.settled_at ?? 0),
      timestamp: proof?.settled_at ?? 0,
      state: activity.status === 'finished' ? 'done' : 'pending',
    },
  ]
}
