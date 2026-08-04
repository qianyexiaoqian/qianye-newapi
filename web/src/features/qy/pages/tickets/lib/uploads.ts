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
 * 一张待提交图片的完整状态。
 *
 * 四个字段合成一个对象而不是四个平行数组，是刻意的：`uploading` 与 `ref` 必须
 * 永远同时翻面（上传中一定还没有 ref，拿到 ref 就一定不在上传中）。拆开之后，
 * 两次 setState 之间会存在一帧"既没在传、也没有 ref、但提交按钮已放行"的窗口，
 * 而那一帧提交出去的请求会少带一张图 —— 用户以为图传上去了，客服看不到。
 */
export type QyTicketImageItem = {
  /** 本地唯一键，只用于 React 列表与增删定位。绝不能用文件名（可以重名）。 */
  key: string
  file: File
  /** 上传成功后的服务端标识；未成功时为 null。 */
  ref: string | null
  uploading: boolean
  /** 上传失败时的错误对象，交给 `qyErrorMessage` 出文案。 */
  error: unknown
}

/** 提交按钮是否该放行：不能有任何一张还在传。 */
export function qyImagesSettled(items: readonly QyTicketImageItem[]): boolean {
  return !items.some((item) => item.uploading)
}

/**
 * 收集可提交的 ref。
 *
 * 失败的那些**直接丢掉**而不是阻塞提交：用户可能就是想把那张传不上去的图放弃、
 * 先把问题说清楚。阻塞提交的话，他唯一的出路是把整条消息重写一遍。
 */
export function qyImageRefs(items: readonly QyTicketImageItem[]): string[] {
  return items
    .map((item) => item.ref)
    .filter((ref): ref is string => ref != null)
}
