package controller

import (
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// redemptionCreatorScope 返回本次请求能看到哪些兑换码：root(role=100) 看全量，
// 其余管理员只看自己发的码。见 model.scopeRedemptionsToCreator 的注释——兑换码
// 明文等同现金，跨管理员可读就等于一个 role=10 能把别人发行的在售码整批收割进
// 自己的钱包，而读取路径一条审计都不写。
func redemptionCreatorScope(c *gin.Context) int {
	if c.GetInt("role") >= common.RoleRootUser {
		return 0
	}
	return c.GetInt("id")
}

// requireOwnRedemption 在按 id 操作单张码时执行同一条判据。
func requireOwnRedemption(c *gin.Context, redemption *model.Redemption) bool {
	scope := redemptionCreatorScope(c)
	if scope == 0 || redemption.UserId == scope {
		return true
	}
	common.ApiErrorI18n(c, i18n.MsgRedemptionInvalid)
	return false
}

func GetAllRedemptions(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	redemptions, total, err := model.GetAllRedemptions(redemptionCreatorScope(c), pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(redemptions)
	common.ApiSuccess(c, pageInfo)
	return
}

func SearchRedemptions(c *gin.Context) {
	keyword := c.Query("keyword")
	status := c.Query("status")
	pageInfo := common.GetPageQuery(c)
	redemptions, total, err := model.SearchRedemptions(redemptionCreatorScope(c), keyword, status, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(redemptions)
	common.ApiSuccess(c, pageInfo)
	return
}

func GetRedemption(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	redemption, err := model.GetRedemptionById(id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !requireOwnRedemption(c, redemption) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    redemption,
	})
	return
}

func AddRedemption(c *gin.Context) {
	if !operation_setting.IsPaymentComplianceConfirmed() {
		common.ApiErrorI18n(c, i18n.MsgPaymentComplianceRequired)
		return
	}

	redemption := model.Redemption{}
	err := c.ShouldBindJSON(&redemption)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if utf8.RuneCountInString(redemption.Name) == 0 || utf8.RuneCountInString(redemption.Name) > 20 {
		common.ApiErrorI18n(c, i18n.MsgRedemptionNameLength)
		return
	}
	if redemption.Count <= 0 {
		common.ApiErrorI18n(c, i18n.MsgRedemptionCountPositive)
		return
	}
	if redemption.Count > 100 {
		common.ApiErrorI18n(c, i18n.MsgRedemptionCountMax)
		return
	}
	if valid, msg := validateExpiredTime(c, redemption.ExpiredTime); !valid {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": msg})
		return
	}
	// 商品类型与它那一侧的必填项一起校验:两种码填错的地方完全不同,
	// 而错误都要在建码这一刻挡住 —— 兑换那一刻再发现,用户手里已经拿着一张废码了。
	productType := redemption.ProductKind()
	quota := redemption.Quota
	planId := 0
	switch productType {
	case model.RedemptionProductQuota:
		if quota <= 0 {
			common.ApiErrorI18n(c, i18n.MsgRedemptionQuotaPositive)
			return
		}
		// 上界与下界同样是硬性的。quota 是 Go int(64 位),库里 redemptions.quota 与
		// users.quota 都是 bigint,而全站的额度语义上界是 common.MaxQuota
		// (= math.MaxInt32,见 common/quota_math.go):所有计费换算、日志/令牌列、
		// 饱和判据都按 int32 立的。没有这道闸时,一个 role=10 管理员可以铸出面额
		// MaxInt64 的码,兑换后 users.quota 直接等于 9223372036854775807 —— 之后
		// 任意一次 `user.Quota += x` 都会在 Go 侧静默回绕成约 -9.2e18 的负余额
		// (aff_transfer 那条路已实测),而这一切既不报错也不留痕。
		// 与 model.TopUp.CreditQuota 的 QuotaFromDecimalStrict 是同一条口径。
		if quota > common.MaxQuota {
			common.ApiErrorI18n(c, i18n.MsgRedemptionQuotaTooLarge)
			return
		}
	case model.RedemptionProductPlan, model.RedemptionProductUserGroup:
		plan, err := model.GetSubscriptionPlanById(redemption.ProductId)
		if err != nil || plan == nil || !plan.Enabled {
			common.ApiErrorI18n(c, i18n.MsgRedemptionPlanInvalid)
			return
		}
		planId = plan.Id
		// 套餐码的额度是死数据:Redeem 的套餐分支压根不读它。这里归零只是不让
		// 运营填的金额被存下来 —— 注意最终落库的其实是 100(quota 列带
		// `default:100`,GORM 把零值整列略过交给数据库),所以读取侧必须靠
		// ProductKind() 判断,不能靠"额度是不是 0"。列表里的额度也据此隐藏。
		quota = 0
	default:
		common.ApiErrorI18n(c, i18n.MsgRedemptionProductTypeInvalid)
		return
	}
	var keys []string
	for i := 0; i < redemption.Count; i++ {
		key := common.GetUUID()
		cleanRedemption := model.Redemption{
			UserId:      c.GetInt("id"),
			Name:        redemption.Name,
			Key:         key,
			CreatedTime: common.GetTimestamp(),
			Quota:       quota,
			ExpiredTime: redemption.ExpiredTime,
			ProductType: productType,
			ProductId:   planId,
		}
		err = cleanRedemption.Insert()
		if err != nil {
			common.SysError("failed to insert redemption: " + err.Error())
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": i18n.T(c, i18n.MsgRedemptionCreateFailed),
				"data":    keys,
			})
			return
		}
		keys = append(keys, key)
	}
	recordManageAudit(c, "redemption.create", map[string]interface{}{
		"name":         redemption.Name,
		"count":        redemption.Count,
		"quota":        logger.LogQuota(quota),
		"product_type": productType,
		"product_id":   planId,
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    keys,
	})
	return
}

func DeleteRedemption(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	existing, err := model.GetRedemptionById(id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !requireOwnRedemption(c, existing) {
		return
	}
	err = model.DeleteRedemptionById(id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
	})
	return
}

func UpdateRedemption(c *gin.Context) {
	statusOnly := c.Query("status_only")
	redemption := model.Redemption{}
	err := c.ShouldBindJSON(&redemption)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	cleanRedemption, err := model.GetRedemptionById(redemption.Id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !requireOwnRedemption(c, cleanRedemption) {
		return
	}
	beforeStatus := cleanRedemption.Status
	beforeQuota := cleanRedemption.Quota
	if statusOnly == "" {
		if valid, msg := validateExpiredTime(c, redemption.ExpiredTime); !valid {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": msg})
			return
		}
		// If you add more fields, please also update redemption.Update()
		cleanRedemption.Name = redemption.Name
		cleanRedemption.ExpiredTime = redemption.ExpiredTime
		// 额度只对余额码有意义。套餐码收下这个值只会在列表里显示一个永远不会
		// 发出去的金额 —— 商品类型本身不可改(见 Redemption.Update),所以这里
		// 认库里那张码的类型,而不是请求体里的。
		if cleanRedemption.ProductKind() == model.RedemptionProductQuota {
			// 与 AddRedemption 同一道闸:改码这一侧原先完全不校验,于是一张
			// 面额可以被改成负数,兑换时直接从用户余额里倒扣,而接口、前端提示
			// 与充值日志三处都在说"充值成功"。建码拦得住、改码拦不住,
			// 等于同一个业务不变量只守住了一半。
			if redemption.Quota <= 0 {
				common.ApiErrorI18n(c, i18n.MsgRedemptionQuotaPositive)
				return
			}
			// 与建码同一条上界:少了它,改码就是绕过建码上界的侧门。
			if redemption.Quota > common.MaxQuota {
				common.ApiErrorI18n(c, i18n.MsgRedemptionQuotaTooLarge)
				return
			}
			// 已兑换是终态,面值同样不可改。钱在兑换那一刻就按旧面值发完了,
			// 之后改这一列不会退钱也不会补钱,只会让三处记录彼此打架:兑换码行上
			// 写着 B、充值日志里记的是 A、用户钱包里进的是 A。事后对账时无从判断
			// 哪一个是事实。上面的 status_only 分支已经把"已兑换不可翻回启用"锁死,
			// 这里是同一条终态口径的另一半 —— 少了它,改面值就是一条绕过状态机的
			// 侧门:status 动不了,但那张码的账面价值可以被任意改写。
			if cleanRedemption.Status == common.RedemptionCodeStatusUsed &&
				redemption.Quota != cleanRedemption.Quota {
				common.ApiErrorI18n(c, i18n.MsgRedemptionUsedImmutable)
				return
			}
			cleanRedemption.Quota = redemption.Quota
		}
	}
	if statusOnly != "" {
		// 状态机:只允许在启用 <-> 禁用之间切换。
		//
		// 已兑换(used)是终态。把它翻回 enabled 会让同一张码被再兑一次 ——
		// 而 redeemed_time / used_user_id 不会被清,第一次兑换的核销痕迹被静默覆盖,
		// 佣金侧的幂等键又是 redemption:<id>,于是钱发两遍、佣金只算一遍,账目自相矛盾。
		// 兄弟接口 controller/token.go 的 status_only 分支本来就有状态机约束,
		// 兑换码这一侧漏了。
		if redemption.Status != common.RedemptionCodeStatusEnabled &&
			redemption.Status != common.RedemptionCodeStatusDisabled {
			common.ApiErrorI18n(c, i18n.MsgRedemptionStatusLocked)
			return
		}
		if cleanRedemption.Status == common.RedemptionCodeStatusUsed {
			common.ApiErrorI18n(c, i18n.MsgRedemptionStatusLocked)
			return
		}
		cleanRedemption.Status = redemption.Status
	}
	err = cleanRedemption.Update()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	// 兑换码是一条发钱通道,改它必须能事后追责。在此之前这个接口只落一条
	// 路由级兜底日志(只有 method/path/status),既没有码 id 也没有前后值 ——
	// 「哪张码被改过面额、哪张被翻回启用」事后无从判断。
	recordManageAudit(c, "redemption.update", map[string]interface{}{
		"redemption_id": cleanRedemption.Id,
		"status_only":   statusOnly != "",
		"status_before": beforeStatus,
		"status_after":  cleanRedemption.Status,
		"quota_before":  logger.LogQuota(beforeQuota),
		"quota_after":   logger.LogQuota(cleanRedemption.Quota),
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    cleanRedemption,
	})
	return
}

func DeleteInvalidRedemption(c *gin.Context) {
	rows, err := model.DeleteInvalidRedemptions()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    rows,
	})
	return
}

func validateExpiredTime(c *gin.Context, expired int64) (bool, string) {
	if expired != 0 && expired < common.GetTimestamp() {
		return false, i18n.T(c, i18n.MsgRedemptionExpireTimeInvalid)
	}
	return true, ""
}
