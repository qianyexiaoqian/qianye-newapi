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
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'

import { QyLotFinePrint } from '../../lottery/components/lottery-fine-print'
import { qyLotMaskRef } from '../../lottery/lib/display'
import {
  qyLotReplayWheel,
  type QyLotProofSpin,
  type QyLotWheelProof,
} from '../../lottery/lib/verify'
import { qyLotTiers, type QyLotSpecItem } from '../../lottery/types'
import { QY_EMPTY_TEXT } from '../../ops/format'
import { qyWheelOutcomeOf, qyWheelTierName } from '../lib/spin'
import { QyWheelTierTable } from './wheel-tier-table'

/** 转动列表默认只画前 N 条；一场几千转的转盘不该把公正性面板撑成一份报表。 */
const PREVIEW_ROWS = 20

/**
 * 公正性面板里转盘那一段：奖档与库存终态、逐转记录、本地库存重放、两条「不保证」。
 *
 * ## 库存重放是这一段存在的理由
 *
 * 批次玩法验的是"名单先冻结、种子后揭示、中奖名单可复算"；转盘多出一样东西要验：
 * 每一次"摇中了但已发完、于是落空"**是真的**。它只能靠按 seq 顺序重放各档库存
 * 递减来证明（design-15 §7.4）—— 揭示种子之后在本机跑一遍 {@link qyLotReplayWheel}，
 * 把重放出的各档终态与文档公布的 `stock_left` 并排印出来，对不上的那一格一眼可见。
 * 它**不请求任何服务端验证接口**，与面板上那颗「在本机复算」按钮同一条纪律。
 *
 * ## 两条「不保证」为什么要印在这里
 *
 * 后端的 `notice` 已经带着它们（折在「这份证据证明了什么」里），这里再用前端文案
 * 印一遍是因为它们是**转盘特有**的边界，而且直接决定用户该怎么读上面那张表：
 * `user_ref` 与真人的对应关系不可外证；并发转动时谁拿到 seq N 与 N+1 由服务端
 * 串行化决定。只声称可复算，不声称不可预知（decisions.md D-13）。
 */
export function QyWheelProofSection(props: { proof: QyLotWheelProof }) {
  const { t } = useTranslation()
  const { proof } = props
  const proofSpins = proof.spins
  const spins = useMemo(
    () => [...(proofSpins ?? [])].sort((a, b) => a.seq - b.seq),
    [proofSpins]
  )
  const [expanded, setExpanded] = useState(false)
  const [replay, setReplay] = useState<{
    stock?: Map<number, number>
    error?: string
  } | null>(null)

  // 只在种子揭示且转动记录取全时重放。分页取回的那一份重放出来的库存是错的，
  // 而错的方向会把一场诚实的转盘印成"库存对不上"。
  const complete = spins.length === proof.total
  const { act_no: actNo, seed, spec: proofSpec } = proof
  useEffect(() => {
    if (seed === '' || !complete || spins.length === 0) {
      setReplay(null)
      return
    }
    let cancelled = false
    void qyLotReplayWheel(seed, actNo, qyLotTiers(proofSpec), spins)
      .then((out) => {
        if (!cancelled) setReplay({ stock: out.stock })
      })
      .catch((error: unknown) => {
        if (!cancelled) setReplay({ error: String(error) })
      })
    return () => {
      cancelled = true
    }
  }, [seed, actNo, proofSpec, spins, complete])

  const outcomeText = (row: QyLotProofSpin): string => {
    const outcome = qyWheelOutcomeOf({
      result_tier: row.tier,
      exhausted_tier: row.exhausted_tier,
    })
    if (outcome === 'none') return t('qy_lot_wheel_tier_none')
    if (outcome === 'exhausted') {
      return t('qy_lot_wheel_row_exhausted', {
        name: qyWheelTierName(proof.spec, row.exhausted_tier),
      })
    }
    return (
      qyWheelTierName(proof.spec, row.tier) ||
      t('qy_lot_tier_no', { no: row.tier })
    )
  }

  // 证据链的 `tiers[]` 带库存终态；把它并回 spec 的形状交给同一张表画。
  const stockByTier = new Map(
    (proof.tiers ?? []).map((tier) => [tier.tier, tier.stock_left])
  )
  const spec: QyLotSpecItem[] = proof.spec.map((item) =>
    item.tier != null && stockByTier.has(item.tier)
      ? { ...item, stock_left: stockByTier.get(item.tier) }
      : item
  )

  const shown = expanded ? spins : spins.slice(0, PREVIEW_ROWS)

  return (
    <div className='space-y-3'>
      <div className='space-y-1.5'>
        <h4 className='text-sm font-medium'>{t('qy_lot_wheel_proof_tiers')}</h4>
        <QyWheelTierTable
          spec={spec}
          replayStock={replay?.stock}
          hideStock={stockByTier.size === 0}
        />
        {replay?.error != null && (
          <p className='text-destructive font-mono text-xs break-all'>
            {replay.error}
          </p>
        )}
        {replay?.stock != null && (
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_wheel_replay_note', { count: spins.length })}
          </p>
        )}
      </div>

      <div className='space-y-1.5'>
        <h4 className='text-sm font-medium'>
          {t('qy_lot_wheel_proof_spins', { count: spins.length })}
        </h4>
        {spins.length === 0 ? (
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_wheel_proof_spins_empty')}
          </p>
        ) : (
          <div className={expanded ? 'max-h-96 overflow-y-auto' : undefined}>
            <StaticDataTable
              data={shown}
              getRowKey={(row: QyLotProofSpin) => row.seq}
              columns={[
                {
                  id: 'seq',
                  header: t('qy_lot_wheel_seq'),
                  cellClassName: 'tabular-nums',
                  cell: (row: QyLotProofSpin) => `#${row.seq}`,
                },
                {
                  id: 'ref',
                  header: t('qy_lot_user_ref'),
                  cell: (row: QyLotProofSpin) => (
                    <span className='font-mono text-xs'>
                      {qyLotMaskRef(row.user_ref)}
                    </span>
                  ),
                },
                {
                  id: 'seed',
                  header: t('qy_lot_wheel_seed_label'),
                  cell: (row: QyLotProofSpin) => (
                    <span className='font-mono text-xs break-all'>
                      {row.client_seed === '' ? QY_EMPTY_TEXT : row.client_seed}
                    </span>
                  ),
                },
                {
                  id: 'ppm',
                  header: t('qy_lot_wheel_ppm'),
                  cellClassName: 'tabular-nums',
                  cell: (row: QyLotProofSpin) => row.ppm,
                },
                {
                  id: 'outcome',
                  header: t('qy_lot_wheel_outcome'),
                  cell: (row: QyLotProofSpin) => outcomeText(row),
                },
              ]}
            />
          </div>
        )}
        {spins.length > PREVIEW_ROWS && (
          <Button
            type='button'
            variant='ghost'
            size='sm'
            onClick={() => setExpanded((prev) => !prev)}
          >
            {expanded
              ? t('qy_lot_wheel_proof_collapse')
              : t('qy_lot_wheel_proof_expand', {
                  count: spins.length - PREVIEW_ROWS,
                })}
          </Button>
        )}
      </div>

      {/* 两条「不保证」原文一字不改，只是折起来：它们是协议边界，不是这一屏
          的决策依据 —— 触发器上先说清里面有几条。 */}
      <QyLotFinePrint label={t('qy_lot_wheel_caveats_label')}>
        <ul className='list-disc space-y-1 ps-4'>
          <li>{t('qy_lot_wheel_caveat_ref')}</li>
          <li>{t('qy_lot_wheel_caveat_order')}</li>
        </ul>
      </QyLotFinePrint>
    </div>
  )
}
