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
import { Eraser, Shuffle } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'

import {
  qyLotBallFormatPick,
  qyLotBallRandomPick,
  type QyLotBallPick,
  type QyLotBallPool,
} from '../lib/ball'

/**
 * 双色球选号盘。
 *
 * ## 为什么机选是纯前端
 *
 * 服务端不区分自选与机选（`qianye/modules/lottery/entry.go` 的 `acceptPick`）：
 * 号码一旦进哈希链，两者的可验证性完全一样，而服务端每多一条随机路径就多一处
 * 需要证明其公正的地方。所以这里的骰子按钮就是本地一次 `crypto.getRandomValues`
 * 抽样，它**只决定用户想买哪一组号**，不参与任何结果判定。
 *
 * ## 为什么选满就锁住而不是自动替换
 *
 * 选满之后再点一个新号，"悄悄换掉最早选的那个"是常见做法，但用户按下确认时
 * 看到的号与他以为的不是同一组——而这张票是要花钱的、不可撤销。所以选满即锁，
 * 想换必须先取消一个，取消这个动作是他自己做的。
 */
export function QyLotBallPicker(props: {
  pool: QyLotBallPool
  value: QyLotBallPick
  onChange: (pick: QyLotBallPick) => void
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const { pool, value } = props

  const toggle = (group: 'blues' | 'reds', ball: number) => {
    const current = value[group]
    const limit = group === 'reds' ? pool.redPick : pool.bluePick
    if (current.includes(ball)) {
      props.onChange({
        ...value,
        [group]: current.filter((item) => item !== ball),
      })
      return
    }
    if (current.length >= limit) return
    props.onChange({
      ...value,
      [group]: [...current, ball].sort((a, b) => a - b),
    })
  }

  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={props.disabled}
          onClick={() => props.onChange(qyLotBallRandomPick(pool))}
        >
          <Shuffle aria-hidden='true' />
          {t('qy_lot_ball_quick_pick')}
        </Button>
        <Button
          type='button'
          variant='ghost'
          size='sm'
          disabled={
            props.disabled ||
            (value.reds.length === 0 && value.blues.length === 0)
          }
          onClick={() => props.onChange({ reds: [], blues: [] })}
        >
          <Eraser aria-hidden='true' />
          {t('qy_lot_ball_clear')}
        </Button>
      </div>

      <BallGroup
        label={t('qy_lot_ball_reds', {
          picked: value.reds.length,
          pick: pool.redPick,
          pool: pool.redPool,
        })}
        tone='red'
        poolSize={pool.redPool}
        limit={pool.redPick}
        selected={value.reds}
        disabled={props.disabled === true}
        onToggle={(ball) => toggle('reds', ball)}
      />
      <BallGroup
        label={t('qy_lot_ball_blues', {
          picked: value.blues.length,
          pick: pool.bluePick,
          pool: pool.bluePool,
        })}
        tone='blue'
        poolSize={pool.bluePool}
        limit={pool.bluePick}
        selected={value.blues}
        disabled={props.disabled === true}
        onToggle={(ball) => toggle('blues', ball)}
      />

      {/* 「我选的号」必须以**进链的那份规范化格式**回显，而不是一堆分散的球：
          用户下一次看到这串字节是在回执与证据链里，两处长得一样他才对得上。 */}
      <div className='rounded-lg border p-3'>
        <Label className='text-muted-foreground text-xs'>
          {t('qy_lot_ball_my_pick')}
        </Label>
        <p className='mt-1 font-mono text-sm break-all tabular-nums'>
          {value.reds.length === 0 && value.blues.length === 0
            ? t('qy_lot_ball_pick_empty')
            : qyLotBallFormatPick(value)}
        </p>
      </div>
    </div>
  )
}

function BallGroup(props: {
  label: string
  tone: 'blue' | 'red'
  poolSize: number
  limit: number
  selected: number[]
  disabled: boolean
  onToggle: (ball: number) => void
}) {
  const balls = Array.from({ length: props.poolSize }, (_, i) => i + 1)
  const full = props.selected.length >= props.limit
  const activeClass =
    props.tone === 'red'
      ? 'border-transparent bg-red-500 text-white'
      : 'border-transparent bg-blue-500 text-white'

  return (
    <div className='space-y-2'>
      <Label className='text-xs'>{props.label}</Label>
      <div className='flex flex-wrap gap-1.5'>
        {balls.map((ball) => {
          const active = props.selected.includes(ball)
          return (
            <button
              key={ball}
              type='button'
              // 选满之后未选中的球置灰而不是"点了就替换掉最早的那个"：
              // 这张票花的是真钱且不可撤销，用户按下确认时看到的号必须就是他
              // 以为的那一组。
              disabled={props.disabled || (full && !active)}
              aria-pressed={active}
              onClick={() => props.onToggle(ball)}
              className={cn(
                'size-9 rounded-full border text-sm font-medium tabular-nums transition-colors',
                'disabled:cursor-not-allowed disabled:opacity-40',
                active ? activeClass : 'hover:bg-muted'
              )}
            >
              {String(ball).padStart(2, '0')}
            </button>
          )
        })}
      </div>
    </div>
  )
}
