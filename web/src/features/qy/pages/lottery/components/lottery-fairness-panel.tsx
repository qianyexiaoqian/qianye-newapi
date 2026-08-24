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
import { QY_EMPTY_TEXT } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { qyLotFullProofQuery, qyLotProofDownloadUrl } from '../api'
import {
  qyLotBands,
  verifyQyLotProof,
  type QyLotVerifyStep,
} from '../lib/verify'
import {
  QY_LOT_PPM_DEN,
  qyLotTiers,
  type QyLotActivityDetail,
  type QyLotProof,
} from '../types'
import { QyLotFinePrint } from './lottery-fine-print'

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
          <QyLotFinePrint label={t('qy_lot_fairness_not_ready')}>
            <p>{t('qy_lot_fairness_not_ready_why')}</p>
          </QyLotFinePrint>
        ) : (
          <>
            <QyTimeline items={buildTimeline(activity, proof, t)} />

            {/*
              四串 64 位十六进制加一个计数，摊开就是 260 多个可见字符 —— 一整屏
              里最长的一块，而它们对**读**这件事零价值：没有人靠肉眼比对哈希。
              它们的价值在「可复制、可截图、可留存」，那三件事展开一次全都做得到。

              所以默认折起来，触发文字直接说明里面有几项。不改成截断显示是刻意的：
              截断之后的截图不再是一份可用的快照，而"在封盘与揭示之间抓一份平台
              无法否认的名单快照"正是这套协议成立的唯一现场动作。
            */}
            <QyLotFinePrint label={t('qy_lot_evidence_digest')}>
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
            </QyLotFinePrint>

            {/* 概率制的档位区间表。**这是概率制相对名次制必须额外公开的东西**：
                名次制里"谁中"由票面排序决定、区间无从谈起；概率制里如果不把
                每一档占据的摇号区间摆出来，用户就只能相信平台说的那个百分比。
                各档区间由 `win_ppm` 按 tier 升序累加得到，前端自己算一遍，
                不用后端下发的数字。 */}
            {proof != null && proof.draw_mode === 'prob' && (
              <div className='space-y-1.5'>
                <h4 className='text-sm font-medium'>
                  {t('qy_lot_band_table')}
                </h4>
                <ul className='space-y-1 text-sm'>
                  {bandRows(proof).map((row) => (
                    <li key={row.tier} className='flex flex-wrap gap-2'>
                      <span className='text-muted-foreground'>
                        {t('qy_lot_tier_no', { no: row.tier })} {row.name}
                      </span>
                      <span className='tabular-nums'>
                        {t('qy_lot_band_range', {
                          lo: row.loPpm,
                          hi: row.hiPpm,
                          percent: row.percent,
                        })}
                      </span>
                    </li>
                  ))}
                </ul>
                <p className='text-muted-foreground text-xs'>
                  {t('qy_lot_no_winner_possible')}
                </p>
              </div>
            )}

            {/* 拿到的条目不完整时**必须说出来**，并且不给"验证通过"的假象。
                这一条不折叠：它说的是"你现在看到的验证结果不可信"，属于
                当场就要知道的事。 */}
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

            {/*
              「复算在本地跑」与「这份证据证不了什么」是同一个问题的两半：
              前者说明这个绿勾凭什么值得信，后者说明它的边界在哪。两条都是
              信任必需而非决策必需 —— 不读它们照样按得下那颗按钮 —— 所以
              合成一个入口折起来，而不是在按钮下面各摆一段灰色小字。

              `notice` 由后端随证据链下发，原样显示：把边界写在证据里比一段
              前端写死的免责声明更难被悄悄改掉。折叠改的是位置，不是来源。
            */}
            <QyLotFinePrint label={t('qy_lot_vf_scope_label')}>
              <p>{t('qy_lot_vf_local_note')}</p>
              {proof != null && (proof.notice ?? '') !== '' && (
                <p className='break-words whitespace-pre-wrap'>
                  {proof.notice}
                </p>
              )}
            </QyLotFinePrint>
          </>
        )}
      </CardContent>
    </Card>
  )
}

/**
 * 各档的摇号区间。
 *
 * `win_ppm` 的和超过 100% 时 `qyLotBands` 会抛错 —— 那说明公示的概率表本身
 * 是错的，这时候渲染一张"看起来没问题"的表就是替平台圆场。所以捕获成空表，
 * 让上面的 `qy_lot_band_table` 一行都不显示，而验证按钮会在 spec 那一步红。
 */
function bandRows(proof: QyLotProof): {
  hiPpm: number
  loPpm: number
  name: string
  percent: string
  tier: number
}[] {
  const tiers = qyLotTiers(proof.spec)
  try {
    return qyLotBands(tiers).map((band) => ({
      tier: band.tier,
      name: tiers.find((tier) => tier.tier === band.tier)?.name ?? '',
      loPpm: band.loPpm,
      hiPpm: band.hiPpm,
      percent: (((band.hiPpm - band.loPpm) / QY_LOT_PPM_DEN) * 100).toFixed(4),
    }))
  } catch {
    return []
  }
}

/**
 * 承诺 → 冻结 → 揭示 → 结算。
 *
 * 未到达的节点保留灰色占位而不是不渲染：用户需要知道"后面还有几步"，
 * 尤其是"种子要到开奖才公布"这一步 —— 那正是整套协议成立的原因。
 *
 * ## 为什么四个节点都不再带说明行
 *
 * `QyTimeline` 每个节点自己就渲染标题 + 时刻，四条标题（承诺 / 冻结名单 /
 * 揭示种子 / 结算完成）连起来已经把顺序讲完了，而顺序正是这条时间线要传达的
 * 全部内容。挂在下面那四句 20 字上下的说明是**同一件事的第二遍**，四条加起来
 * 60 多个字，在窄屏上把时间线撑成两屏高。协议本身的说明留在卡片头部那一句与
 * 底部的「这份证据证明什么」折叠位里。
 *
 * 结算那一条此前更是把 `settled_at` **同时**写进 description 与 timestamp，
 * 屏幕上一行里出现两个一模一样的时间戳 —— 那不是精简掉的信息，是重复。
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
      timestamp: activity.open_at,
      state: activity.commit_hash === '' ? 'pending' : 'done',
    },
    {
      key: 'freeze',
      title: t('qy_lot_tl_freeze'),
      // 显示**实际**封盘时刻而不是计划的 close_at：它与揭示时刻之间的那一段
      // 才是"任何人都能抓一份平台无法否认的名单快照"的窗口，而封盘任务落后时
      // 这两个值可以差很远。
      timestamp: proof?.locked_at ?? activity.close_at,
      state: proof != null && proof.roster_hash !== '' ? 'done' : 'pending',
    },
    {
      key: 'reveal',
      title: t('qy_lot_tl_reveal'),
      timestamp: proof?.revealed_at ?? activity.draw_at,
      state: revealed ? 'done' : 'pending',
    },
    {
      key: 'settle',
      title: t('qy_lot_tl_settle'),
      timestamp: proof?.settled_at ?? 0,
      state: activity.status === 'finished' ? 'done' : 'pending',
    },
  ]
}
