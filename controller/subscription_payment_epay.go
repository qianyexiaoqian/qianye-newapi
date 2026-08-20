package controller

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

type SubscriptionEpayPayRequest struct {
	PlanId        int    `json:"plan_id"`
	PaymentMethod string `json:"payment_method"`
}

func SubscriptionRequestEpay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}

	var req SubscriptionEpayPayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	err = model.QyGateSubscriptionSeat(nil, plan, c.GetInt("id"), "precheck", err)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !plan.Enabled {
		common.ApiErrorMsg(c, "套餐未启用")
		return
	}
	// 发售时间窗:与 !plan.Enabled 并列的第二道下单闸门。四个网关各写一遍而不是
	// 收进一个共享 helper,是因为这四个 handler 本来就逐行同构(取套餐 → 名额预检
	// → 启用检查 → 各自的 product id 检查),抽走其中一句反而让"这里一共挡了几件事"
	// 变得要跳文件才看得清。
	if err := model.PlanSaleWindowError(plan, common.GetTimestamp()); err != nil {
		common.ApiError(c, err)
		return
	}
	if plan.PriceAmount < 0.01 {
		common.ApiErrorMsg(c, "套餐金额过低")
		return
	}
	if !operation_setting.ContainsPayMethod(req.PaymentMethod) {
		common.ApiErrorMsg(c, "支付方式不存在")
		return
	}

	userId := c.GetInt("id")
	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			common.ApiErrorMsg(c, "已达到该套餐购买上限")
			return
		}
	}
	// 用户组商品的「你已经永久拥有该用户组」这一条,必须在**下单之前**问。
	//
	// 它在 CreateUserSubscriptionFromPlanTx 里是一条 return error,而支付回调
	// 走的就是那个函数 —— 付款之后才撞上它,整个事务回滚、订单永久停在 pending、
	// 钱收了货发不出。现在回调侧对已付款的一档改成放行(见 isPaidSubscriptionSource),
	// 但那是兜底;真正该拦住用户的位置是这里,钱还没出的时候。
	//
	// 四个网关各写一遍,与上面那两道闸门同样的理由:这个 handler 一共挡了几件事,
	// 应该在同一屏里看得完。
	if preview, err := model.PreviewUserGroupPurchase(userId, plan); err != nil {
		common.ApiError(c, err)
		return
	} else if preview.Action == model.UserGroupPurchaseActionReject {
		common.ApiErrorMsg(c, preview.Message)
		return
	}

	callBackAddress := service.GetCallbackAddress()
	returnUrl, err := url.Parse(callBackAddress + "/api/subscription/epay/return")
	if err != nil {
		common.ApiErrorMsg(c, "回调地址配置错误")
		return
	}
	notifyUrl, err := url.Parse(callBackAddress + "/api/subscription/epay/notify")
	if err != nil {
		common.ApiErrorMsg(c, "回调地址配置错误")
		return
	}

	tradeNo := fmt.Sprintf("%s%d", common.GetRandomString(6), time.Now().Unix())
	tradeNo = fmt.Sprintf("SUBUSR%dNO%s", userId, tradeNo)

	client := GetEpayClient()
	if client == nil {
		common.ApiErrorMsg(c, "当前管理员未配置支付信息")
		return
	}

	// 网关金额必须过 operation_setting.Price。
	//
	// plan.price_amount 在本仓的三条口径里是「单位」而不是「元」:余额购买
	// calcSubscriptionBalanceQuota 拿它 × QuotaPerUnit 换额度,前端购买弹窗同式,
	// 佣金 topUpBaseQuota 对订阅单也按 Money × QuotaPerUnit 算基数;而钱包充值
	// getPayMoney 对同一个「单位」是要乘汇率的。订阅走 epay 时原样把它当网关金额
	// 发出去 —— Price 取仓库默认 7.3 时,同一件商品现金付 ¥30、余额付相当于 ¥219,
	// 现金收入缩水到 1/Price,而 upsertSubscriptionTopUpTx 写下的那条 provider=''
	// 的 top_ups 行会把同一个误差原样放大到佣金支出。
	payMoney := subscriptionEpayMoney(plan.PriceAmount)

	order := &model.SubscriptionOrder{
		UserId:          userId,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         tradeNo,
		PaymentMethod:   req.PaymentMethod,
		PaymentProvider: model.PaymentProviderEpay,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
		// 下单那一刻的套餐随订单走。回调按快照发货,运营在用户付款途中改这张
		// 套餐的价格/额度/时长/升级组不会改变这一单的内容。见 PlanSnapshot。
		PlanSnapshot: model.SubscriptionPlanSnapshot(plan),
	}
	if err := order.Insert(); err != nil {
		common.ApiErrorMsg(c, "创建订单失败")
		return
	}
	uri, params, err := client.Purchase(&epay.PurchaseArgs{
		Type:           req.PaymentMethod,
		ServiceTradeNo: tradeNo,
		Name:           fmt.Sprintf("SUB:%s", plan.Title),
		Money:          strconv.FormatFloat(payMoney, 'f', 2, 64),
		Device:         epay.PC,
		NotifyUrl:      notifyUrl,
		ReturnUrl:      returnUrl,
	})
	if err != nil {
		_ = model.ExpireSubscriptionOrder(tradeNo, model.PaymentProviderEpay)
		common.ApiErrorMsg(c, "拉起支付失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": params, "url": uri})
}

func SubscriptionEpayNotify(c *gin.Context) {
	var params map[string]string

	if c.Request.Method == "POST" {
		// POST 请求：从 POST body 解析参数
		if err := c.Request.ParseForm(); err != nil {
			_, _ = c.Writer.Write([]byte("fail"))
			return
		}
		params = lo.Reduce(lo.Keys(c.Request.PostForm), func(r map[string]string, t string, i int) map[string]string {
			r[t] = c.Request.PostForm.Get(t)
			return r
		}, map[string]string{})
	} else {
		// GET 请求：从 URL Query 解析参数
		params = lo.Reduce(lo.Keys(c.Request.URL.Query()), func(r map[string]string, t string, i int) map[string]string {
			r[t] = c.Request.URL.Query().Get(t)
			return r
		}, map[string]string{})
	}

	if len(params) == 0 {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	client := GetEpayClient()
	if client == nil {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}
	verifyInfo, err := client.Verify(params)
	if err != nil || !verifyInfo.VerifyStatus {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	if verifyInfo.TradeStatus != epay.StatusTradeSuccess {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	LockOrder(verifyInfo.ServiceTradeNo)
	defer UnlockOrder(verifyInfo.ServiceTradeNo)

	if err := model.CompleteSubscriptionOrder(verifyInfo.ServiceTradeNo, common.GetJsonString(verifyInfo), model.PaymentProviderEpay, verifyInfo.Type); err != nil {
		_, _ = c.Writer.Write([]byte("fail"))
		return
	}

	_, _ = c.Writer.Write([]byte("success"))
}

// SubscriptionEpayReturn handles browser return after payment.
// It verifies the payload and completes the order, then redirects to console.
func SubscriptionEpayReturn(c *gin.Context) {
	var params map[string]string

	if c.Request.Method == "POST" {
		// POST 请求：从 POST body 解析参数
		if err := c.Request.ParseForm(); err != nil {
			c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
			return
		}
		params = lo.Reduce(lo.Keys(c.Request.PostForm), func(r map[string]string, t string, i int) map[string]string {
			r[t] = c.Request.PostForm.Get(t)
			return r
		}, map[string]string{})
	} else {
		// GET 请求：从 URL Query 解析参数
		params = lo.Reduce(lo.Keys(c.Request.URL.Query()), func(r map[string]string, t string, i int) map[string]string {
			r[t] = c.Request.URL.Query().Get(t)
			return r
		}, map[string]string{})
	}

	if len(params) == 0 {
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
		return
	}

	client := GetEpayClient()
	if client == nil {
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
		return
	}
	verifyInfo, err := client.Verify(params)
	if err != nil || !verifyInfo.VerifyStatus {
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
		return
	}
	if verifyInfo.TradeStatus == epay.StatusTradeSuccess {
		LockOrder(verifyInfo.ServiceTradeNo)
		defer UnlockOrder(verifyInfo.ServiceTradeNo)
		if err := model.CompleteSubscriptionOrder(verifyInfo.ServiceTradeNo, common.GetJsonString(verifyInfo), model.PaymentProviderEpay, verifyInfo.Type); err != nil {
			c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=fail"))
			return
		}
		c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=success"))
		return
	}
	c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=pending"))
}

// subscriptionEpayMoney 把套餐标价（与钱包充值的 amount 同一个「单位」）换算成
// 网关要收的金额，口径与 controller/topup.go 的 getPayMoney 一致。
//
// 刻意不复用 getPayMoney:后者还要乘充值分组倍率与档位折扣，那两项是「充值」
// 这个动作的促销参数，套餐有自己的定价，混进来会让同一张套餐对不同用户卖出
// 不同的现金价而订单上却记着同一个 price_amount。
func subscriptionEpayMoney(priceAmount float64) float64 {
	price := operation_setting.Price
	if price <= 0 {
		price = 1
	}
	return decimal.NewFromFloat(priceAmount).Mul(decimal.NewFromFloat(price)).InexactFloat64()
}
