/*
 * 商品列表真的挂起来之后，一屏里该有的东西。
 *
 * 守的是三件在类型上看不见的事：
 *   1. 价格走星屑组件（千分位 + 单位名），而不是被换算成美元；
 *   2. "不能买的原因"写在卡上 —— 售罄与限购是两句不同的话，且限购**不在**
 *      后端的 available 里，前端得自己判；
 *   3. 点开详情只发一条详情请求，而且详情里出现「立即兑换」。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { cleanupQyMallScreens, mountQyMallScreen, zhKeys } from './mall-harness'

const { QyMallProductsBody } = await import('../index')

after(async () => {
  await cleanupQyMallScreens()
})

const PRODUCTS = [
  {
    product_no: 'P-code',
    kind: 'code',
    title: '一张第三方卡密',
    description: '',
    cover_url: '',
    price: 1200,
    stock: 5,
    sold: 2,
    per_user_limit: 0,
    sale_start_at: 0,
    sale_end_at: 0,
    plan_id: 0,
    plan: null,
    available: true,
    my_count: 0,
  },
  {
    product_no: 'P-phys',
    kind: 'physical',
    title: '一件周边实物',
    description: '',
    cover_url: '',
    price: 300,
    stock: 3,
    sold: 3,
    per_user_limit: 0,
    sale_start_at: 0,
    sale_end_at: 0,
    plan_id: 0,
    plan: null,
    available: false,
    my_count: 0,
  },
  {
    product_no: 'P-plan',
    kind: 'plan',
    title: '一份月卡套餐',
    description: '',
    cover_url: '',
    price: 500,
    stock: -1,
    sold: 9,
    per_user_limit: 1,
    sale_start_at: 0,
    sale_end_at: 0,
    plan_id: 3,
    plan: {
      title: '月卡',
      price_amount: 10,
      upgrade_group: 'vip',
      no_quota: true,
      duration_unit: 'month',
      duration_value: 1,
    },
    // 后端的 available 不含限购：这一行它说"能买"，前端要自己看 my_count。
    available: true,
    my_count: 1,
  },
]

function respond(req: { url: string }) {
  if (req.url.includes('/mall/products/P-code')) {
    return { ...PRODUCTS[0], preview: null }
  }
  if (req.url.includes('/mall/products')) {
    return { items: PRODUCTS, total: PRODUCTS.length, page: 1, page_size: 12 }
  }
  return undefined
}

describe('商品列表', () => {
  test('三张卡各自带价格、售罄与限购的理由', async () => {
    const screen = await mountQyMallScreen({
      element: <QyMallProductsBody />,
      respond,
    })

    const text = screen.text()
    for (const product of PRODUCTS) {
      assert.ok(text.includes(product.title), `没渲染出 ${product.title}`)
    }
    assert.ok(
      text.includes(`1,200 ${zhKeys.qy_sd_unit_default}`),
      `价格必须是"千分位 + 星屑单位名"，实际：${text}`
    )
    assert.ok(
      !text.includes('$'),
      '星屑是整数积分，卡片上出现美元符号就是走错了金额组件'
    )
    assert.ok(text.includes(zhKeys.qy_ml_block_sold_out), '售罄那张没说售罄')
    assert.ok(
      text.includes(zhKeys.qy_ml_block_limit),
      '限购已满那张没说"已达限购" —— 后端 available=true，这一句只能由前端判'
    )

    // 余量画成条(task-C-visual):兑换码剩 5 枚满条,实物 3/3 已卖完 → 斜纹终态,
    // 不限库存的套餐不画。判据是 progressbar 的 aria 值,与条子共用同一份事实。
    const bars = [...screen.container.querySelectorAll('[role="progressbar"]')]
    assert.deepEqual(
      bars.map((bar) => [
        bar.getAttribute('aria-valuenow'),
        bar.getAttribute('data-empty'),
      ]),
      [
        ['5', null],
        ['0', 'true'],
      ],
      '两根余量条:兑换码 5 枚在售、实物已售罄;不限库存的套餐没有条'
    )

    const lists = screen.sent.filter(
      (row) => row.url.endsWith('/mall/products') && row.method === 'GET'
    )
    assert.equal(lists.length, 1, '一进页面只该拉一次列表')
    assert.deepEqual(lists[0].params, { page: 1, page_size: 12 })
  })

  test('点开详情：只发一条详情请求，弹窗里有「立即兑换」', async () => {
    const screen = await mountQyMallScreen({
      element: <QyMallProductsBody />,
      respond,
    })
    const before = screen.sent.length

    assert.ok(
      await screen.click(zhKeys.qy_common_detail),
      '卡片上没有「查看详情」按钮'
    )

    const details = screen.sent
      .slice(before)
      .filter((row) => row.url.includes('/mall/products/'))
    assert.deepEqual(
      details.map((row) => row.url.split('/mall/products/')[1]),
      ['P-code'],
      '点第一张卡应当只拉 P-code 的详情'
    )
    assert.ok(
      screen.buttons().includes(zhKeys.qy_ml_buy),
      `详情弹窗里没有「立即兑换」：${screen.buttons().join(' | ')}`
    )
  })
})
