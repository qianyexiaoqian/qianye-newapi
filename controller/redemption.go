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

func GetAllRedemptions(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	redemptions, total, err := model.GetAllRedemptions(pageInfo.GetStartIdx(), pageInfo.GetPageSize())
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
	redemptions, total, err := model.SearchRedemptions(keyword, status, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
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
	err := model.DeleteRedemptionById(id)
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
