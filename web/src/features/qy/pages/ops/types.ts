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
/**
 * 运维页面共用的后端结构。
 *
 * `hot_queue` 来自 `guard.QueueStats()`，目前的唯一出口是 `/admin/health`
 * （`/admin/availability/stats` 已随管理端可用率页一并移除）。类型仍收敛在这里：
 * 它是后端一份结构的镜像，下一个消费方直接引用即可，不必再抄一遍。
 */

/**
 * 热路径异步队列水位。
 *
 * `dropped > 0` 是全扩展唯一会造成「用户该拿的钱没拿到」的路径：
 * 队列满时事件被丢弃，佣金/违规记录/可用率采样直接消失。
 * 任何展示它的页面都必须把非零值标红告警，不能只当成一个数字。
 */
export type QyHotQueueStats = {
  capacity: number
  pending: number
  submitted: number
  dropped: number
}
