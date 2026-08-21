package model

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"gorm.io/gorm"
)

// 兑换码的商品类型。一张码只绑一种,不做组合包。
const (
	// RedemptionProductQuota 余额:直接给用户加额度,兑换码原本的唯一形态。
	RedemptionProductQuota = "quota"
	// RedemptionProductPlan 订阅套餐:发一条 UserSubscription。
	RedemptionProductPlan = "plan"
	// RedemptionProductUserGroup 用户组商品:同样是发一条 UserSubscription,
	// 只是它绑的那个套餐是纯商品档(SubscriptionPlan.NoQuota)—— 不带余额、只改用户组。
	//
	// 它与 plan 共用同一条发放路径是**刻意的**:两者的差别整个落在套餐本身,
	// 而不在兑换码这一侧。给它们各写一条发放路径,只会让 upgrade_group /
	// prev_user_group / no_quota 这些快照字段有两处需要同步维护。
	RedemptionProductUserGroup = "usergroup"
)

// RedemptionSubscriptionSource 是兑换码发出的订阅在 user_subscriptions.source 里的取值。
// 与 "order"(在线支付)、"admin"(管理端手动绑定)并列,用来在事后分辨这条订阅的来路。
const RedemptionSubscriptionSource = "redemption"

type Redemption struct {
	Id           int            `json:"id"`
	UserId       int            `json:"user_id"`
	Key          string         `json:"key" gorm:"type:char(32);uniqueIndex"`
	Status       int            `json:"status" gorm:"default:1"`
	Name         string         `json:"name" gorm:"index"`
	Quota        int            `json:"quota" gorm:"default:100"`
	CreatedTime  int64          `json:"created_time" gorm:"bigint"`
	RedeemedTime int64          `json:"redeemed_time" gorm:"bigint"`
	Count        int            `json:"count" gorm:"-:all"` // only for api request
	UsedUserId   int            `json:"used_user_id"`
	DeletedAt    gorm.DeletedAt `gorm:"index"`
	ExpiredTime  int64          `json:"expired_time" gorm:"bigint"` // 过期时间，0 表示不过期

	// ProductType 这张码兑换的是什么商品。取值见 RedemptionProduct* 常量。
	// 判定一律走 ProductKind(),不要直接比较这个字段 —— 存量数据是空串。
	//
	// ⚠ 这一列非 quota 时,上面的 Quota 是**没有意义的死数据**,而且不一定是 0:
	// quota 列带 `default:100`,GORM 对带默认值的列会把零值整列略过、交给数据库
	// 补默认值,所以建套餐码时那个刻意的 0 落库其实是 100。想读额度的地方
	// 必须先过 ProductKind(),不能只看 Quota 是不是零。
	ProductType string `json:"product_type" gorm:"type:varchar(16);default:'quota'"`
	// ProductId 商品类型是 plan / usergroup 时指向 subscription_plans.id;余额码恒为 0。
	ProductId int `json:"product_id" gorm:"default:0"`
}

// ProductKind 返回这张码实际要发放的商品类型。
//
// 存量兑换码建于 product_type 这一列存在之前,值是空串,必须按余额码处理 ——
// 否则升级那一刻,库里所有还没兑换的码会一起变成"商品类型不认识"。
// 列上的 default:'quota' 只管新插入的行,救不了已经在库里的那些,
// 所以判定只认这一处,不认那个默认值。
func (redemption *Redemption) ProductKind() string {
	kind := strings.TrimSpace(redemption.ProductType)
	if kind == "" {
		return RedemptionProductQuota
	}
	return kind
}

// RedeemResult 描述一次成功的兑换到底给了用户什么。
//
// 兑换码不再只发余额之后,单独一个 quota int 已经表达不了结果:套餐码一分额度都不加,
// 它给的是一条订阅,可能还顺带把用户组升了。调用方应当按 ProductType 分支,
// 而不是靠"quota 是不是 0"去猜 —— 一张 0 额度的余额码同样满足那个条件。
type RedeemResult struct {
	ProductType string `json:"product_type"`
	// Quota 本次真正加进钱包的额度。套餐 / 用户组码恒为 0。
	Quota int `json:"quota"`
	// PlanId、PlanTitle 只有套餐 / 用户组码有值。
	PlanId    int    `json:"plan_id,omitempty"`
	PlanTitle string `json:"plan_title,omitempty"`
	// UpgradeGroup 非空表示这次兑换确实把用户组升到了这里(套餐配了升级组、
	// 而用户原本不在该组)。用户组商品的兑换成功提示要靠它才说得具体。
	UpgradeGroup string `json:"upgrade_group,omitempty"`
}

// scopeRedemptionsToCreator 把兑换码查询收窄到某个发码人名下。
//
// creatorId == 0 表示不收窄（只有 role=100 走这一支）。
//
// 兑换码是**可直接兑成余额的 bearer 凭据**：拿到那 32 位明文就等于拿到了钱。
// 而管理端列表/搜索/详情原样吐出未兑换码的明文，于是任意 role=10 账号可以把
// 别的管理员（或 root）已经发行、准备分给客户的整批在售码一次性读走再自己兑
// 掉，事后只剩 used_user_id 一个间接线索。上一轮把这条判成“role=10 本来就能
// 建码，不新增能力”——那个理由只覆盖“建自己的码”，不覆盖“收割别人已发行的
// 码”：被收割的是站点对客户的未兑现负债，不是他自己造的价值。
//
// 收窄到发码人而不是遮挡明文，是因为发码/补发流程（含前端批量复制）本来就
// 需要明文，遮挡会把正常业务一起掐掉；而按发码人分桶既保住了流程，又让
// “读别人的码”这件事根本不存在。root 仍可见全量，运营兜底不受影响。
func scopeRedemptionsToCreator(query *gorm.DB, creatorId int) *gorm.DB {
	if creatorId <= 0 {
		return query
	}
	return query.Where("user_id = ?", creatorId)
}

func GetAllRedemptions(creatorId int, startIdx int, num int) (redemptions []*Redemption, total int64, err error) {
	// 开始事务
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// 获取总数
	err = scopeRedemptionsToCreator(tx.Model(&Redemption{}), creatorId).Count(&total).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// 获取分页数据
	err = scopeRedemptionsToCreator(tx.Model(&Redemption{}), creatorId).
		Order("id desc").Limit(num).Offset(startIdx).Find(&redemptions).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// 提交事务
	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return redemptions, total, nil
}

func SearchRedemptions(creatorId int, keyword string, status string, startIdx int, num int) (redemptions []*Redemption, total int64, err error) {
	tx := DB.Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	query := scopeRedemptionsToCreator(tx.Model(&Redemption{}), creatorId)

	if keyword != "" {
		where := "name LIKE ?"
		args := []interface{}{keyword + "%"}
		if id, err := strconv.Atoi(keyword); err == nil {
			where = "id = ? OR " + where
			args = append([]interface{}{id}, args...)
		}
		// 兑换码在日志与错误文本里只留末 4 位(common.MaskCredential)。用户报来的
		// 就是那 4 位,而在此之前后台只能按名称和 id 搜 —— 于是"客服凭末 4 位把码
		// 对上库里那一行"这句话在产品里没有任何落点,掩码等于把线索也一起掩掉了。
		//
		// 判据刻意收紧成「十六进制片段、长度 4..32」:
		//   - 生成的码是 uuid v4 去掉横线,恒为 32 位小写十六进制,非十六进制的
		//     输入不可能是码片段,让它去撞 key 只会白扫一遍全表;
		//   - 十六进制里没有 % 和 _,顺带堵死从搜索框注入 LIKE 通配符;
		//   - 4 位以下的片段在 65536 的空间里命中太多,给不出可用的答案。
		// 匹配走后缀(`%片段`)而不是包含,因为掩码留的就是后缀。
		//
		// 大小写:统一转小写再比。三个数据库的 LIKE 大小写规则并不一致
		// (PostgreSQL 区分大小写,MySQL 的 utf8mb4 通用排序规则和 SQLite 的
		// ASCII LIKE 不区分),而生成的码本来就只有小写,统一到小写这一侧
		// 三个库的行为才一致。
		fragment := strings.ToLower(strings.TrimSpace(keyword))
		if len(fragment) >= 4 && len(fragment) <= 32 {
			hexOnly := true
			for i := 0; i < len(fragment); i++ {
				c := fragment[i]
				if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
					hexOnly = false
					break
				}
			}
			if hexOnly {
				where += " OR LOWER(" + commonKeyCol + ") LIKE ?"
				args = append(args, "%"+fragment)
			}
		}
		query = query.Where(where, args...)
	}

	if status != "" {
		now := common.GetTimestamp()
		switch status {
		case "expired":
			query = query.Where(
				"status = ? AND expired_time != 0 AND expired_time < ?",
				common.RedemptionCodeStatusEnabled,
				now,
			)
		case strconv.Itoa(common.RedemptionCodeStatusEnabled):
			query = query.Where(
				"status = ? AND (expired_time = 0 OR expired_time >= ?)",
				common.RedemptionCodeStatusEnabled,
				now,
			)
		case strconv.Itoa(common.RedemptionCodeStatusDisabled):
			query = query.Where("status = ?", common.RedemptionCodeStatusDisabled)
		case strconv.Itoa(common.RedemptionCodeStatusUsed):
			query = query.Where("status = ?", common.RedemptionCodeStatusUsed)
		}
	}

	// Get total count
	err = query.Count(&total).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	// Get paginated data
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&redemptions).Error
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}

	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}

	return redemptions, total, nil
}

func GetRedemptionById(id int) (*Redemption, error) {
	if id == 0 {
		return nil, errors.New("id 为空！")
	}
	redemption := Redemption{Id: id}
	var err error = nil
	err = DB.First(&redemption, "id = ?", id).Error
	return &redemption, err
}

func Redeem(key string, userId int) (*RedeemResult, error) {
	if key == "" {
		return nil, errors.New("未提供兑换码")
	}
	if userId == 0 {
		return nil, errors.New("无效的 user id")
	}
	redemption := &Redemption{}

	keyCol := "`key`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		keyCol = `"key"`
	}
	// plan 只有套餐 / 用户组码会被赋值,兑换成功后靠它区分两条收尾路径。
	var plan *SubscriptionPlan
	var subscription *UserSubscription
	common.RandomSleep()
	err := DB.Transaction(func(tx *gorm.DB) error {
		err := lockForUpdate(tx).Where(keyCol+" = ?", key).First(redemption).Error
		if err != nil {
			return errors.New("无效的兑换码")
		}
		if redemption.Status != common.RedemptionCodeStatusEnabled {
			return errors.New("该兑换码已被使用")
		}
		// 核销痕迹是比 status 更硬的判据:status 是一列可以被管理端写回去的值,
		// redeemed_time / used_user_id 记的是"这张码确实已经发过货"这个事实。
		// 一张码只兑一次是下游依赖的不变量 —— 佣金幂等键 redemption:<id> 正建立
		// 在它之上 —— 所以最后一道闸必须落在真正动钱的这一层,而不是只落在入口校验上。
		if redemption.RedeemedTime != 0 || redemption.UsedUserId != 0 {
			return errors.New("该兑换码已被使用")
		}
		if redemption.ExpiredTime != 0 && redemption.ExpiredTime < common.GetTimestamp() {
			return errors.New("该兑换码已过期")
		}
		productKind := redemption.ProductKind()
		if productKind == RedemptionProductQuota && redemption.Quota <= 0 {
			// 余额码的面额必须为正。非正面额兑换出去的是一笔倒扣:用户余额被扣走,
			// 而接口回 success、日志记的是一条"充值"。建码侧拦得住,这里是动钱前的最后一道。
			return errors.New("该兑换码额度无效")
		}
		if productKind != RedemptionProductQuota {
			// 套餐在 CAS 之前查:套餐被删或被停用时,这次兑换要整个失败,
			// 而不是把码标成已用再发现发不出货。反正同一事务,报错即回滚。
			if productKind != RedemptionProductPlan && productKind != RedemptionProductUserGroup {
				return fmt.Errorf("未知的兑换码商品类型: %s", productKind)
			}
			plan, err = getSubscriptionPlanByIdTx(tx, redemption.ProductId)
			if err != nil {
				return err
			}
			if !plan.Enabled {
				return errors.New("套餐未启用")
			}
			// 停售/未开售同样挡住兑换码,判据与手动下架(上面那句)一致 ——
			// 时间窗本来就是"到点自动 enabled",两者对兑换码的效果理应相同。
			//
			// 这一句必须排在下面的 CAS **之前**:排在之后的话,码已经被标成 used,
			// 虽然同事务回滚能救回来,但那是靠"恰好在一个事务里"这个偶然,
			// 与上面那条注释所说的顺序理由是同一条。
			//
			// 挡住不消耗兑换码:失败即回滚,码仍然是 enabled,运营把停售时间改掉
			// 之后它照样能用。
			if err := PlanSaleWindowError(plan, common.GetTimestamp()); err != nil {
				return err
			}
		}
		// Compare-and-swap on status: only the transaction that flips
		// enabled -> used may credit quota, so a concurrent redeem of the
		// same code loses here even without a row lock (e.g. on SQLite).
		result := tx.Model(&Redemption{}).
			Where("id = ? AND status = ? AND redeemed_time = 0 AND used_user_id = 0",
				redemption.Id, common.RedemptionCodeStatusEnabled).
			Updates(map[string]interface{}{
				"redeemed_time": common.GetTimestamp(),
				"status":        common.RedemptionCodeStatusUsed,
				"used_user_id":  userId,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errors.New("该兑换码已被使用")
		}
		if plan == nil {
			// 加额度这一步必须确认真的落到了一行上。此前它只看 Error:收款人不存在
			// (会话还在、用户已被软删)时 UPDATE 匹配 0 行、不报错,事务照常提交 ——
			// 码被 CAS 消耗掉、状态写成 used、面值一分钱没进任何人的钱包,而接口回
			// success、前端弹"已到账"、日志记一条充值。用户手里的码作废了,东西没拿到,
			// 而且没有任何一处会报错。套餐分支下面那句 lockForUpdate(...).First(&userRow)
			// 早就有这道闸(查不到就 ErrRecordNotFound),余额分支漏了同一条。
			//
			// 用 RowsAffected 作判据是安全的:上面已经拒掉了 Quota <= 0,加数恒 >= 1,
			// 所以"匹配到了但值没变"这种会让 MySQL 报 0 行的情形在这里不存在。
			credit := tx.Model(&User{}).Where("id = ?", userId).
				Update("quota", gorm.Expr("quota + ?", redemption.Quota))
			if credit.Error != nil {
				return credit.Error
			}
			if credit.RowsAffected == 0 {
				return errors.New("兑换码持有者不存在")
			}
			return nil
		}
		// 发订阅必须与上面那次 CAS 在**同一个事务**里:拆成两步的话,中间任何一次
		// 失败都会留下"码已经标成已用、订阅却没发出去"——用户手里的码作废了,东西没拿到。
		//
		// 先锁用户行,与 CompleteSubscriptionOrder / AdminBindSubscription 一致:
		// 让 CreateUserSubscriptionFromPlanTx 里的 MaxPurchasePerUser 检查按用户串行,
		// 否则同一个人同时兑换两张同套餐的码可以一起穿过购买上限。
		var userRow User
		if err := lockForUpdate(tx).Select("id").Where("id = ?", userId).First(&userRow).Error; err != nil {
			return err
		}
		subscription, err = CreateUserSubscriptionFromPlanTx(tx, userId, plan, RedemptionSubscriptionSource)
		return err
	})
	if err != nil {
		// 兑换失败**不消耗**兑换码 —— 走到这里那张码仍然是 enabled、面值分文未动,
		// 所以这条日志一旦带上码本身,就等于把一张活码送给任何有日志读取权的人
		// (运维、采集平台、日志备份)。
		//
		// 这里可以直接写 err.Error(),判据是逐条查过的:本闭包里每一个 return 都是
		// 上面那几处自己写死的中文文本,不含 key;而下游 GORM / 驱动返回的错误也带
		// 不出它 —— 查询用的是绑定参数(`key` = ?),三个驱动的错误文本都不回显
		// 绑定值。唯一会回显值的是 MySQL 的唯一键冲突(`Duplicate entry 'x'`),
		// 而这条路径上没有任何一次插入。新增分支时这条判据要重新过一遍。
		common.SysError("redemption failed: " + err.Error())
		return nil, ErrRedeemFailed
	}
	if plan == nil {
		syncCreditUserQuotaCache(userId, redemption.Quota, "redemption")
		RecordLog(userId, LogTypeTopup, fmt.Sprintf("通过兑换码充值 %s，兑换码ID %d", logger.LogQuota(redemption.Quota), redemption.Id))
		QyOnRedeemSuccess(userId, redemption.Id, redemption.Quota)
		return &RedeemResult{ProductType: RedemptionProductQuota, Quota: redemption.Quota}, nil
	}
	// 套餐 / 用户组码没有一分钱进钱包,所以既不同步额度缓存,也不触发充值返佣。
	//
	// QyOnRedeemSuccess 尤其不能顺手带上:它按**充值额度**给邀请人分成,而套餐码的
	// redemption.Quota 并不是 0 —— 那一列带 `default:100`,建码时写的 0 被数据库
	// 换成了默认值(见 Redemption.ProductType 上的说明)。照抄余额码那三行的话,
	// 每张套餐码都会按一笔从未发生过的充值给上线结一次佣。
	upgradeGroup := ""
	if subscription.PrevUserGroup != "" {
		// PrevUserGroup 非空正是 CreateUserSubscriptionFromPlanTx 里"确实执行了升级"
		// 的标记 —— 用户本来就在目标组时它是空的,那种情况没必要刷缓存。
		upgradeGroup = strings.TrimSpace(subscription.UpgradeGroup)
		refreshSubscriptionUserGroupCache(userId, "redemption subscription")
	}
	RecordLog(userId, LogTypeTopup, fmt.Sprintf("通过兑换码开通订阅 %s，兑换码ID %d", plan.Title, redemption.Id))
	return &RedeemResult{
		ProductType:  redemption.ProductKind(),
		PlanId:       plan.Id,
		PlanTitle:    plan.Title,
		UpgradeGroup: upgradeGroup,
	}, nil
}

func (redemption *Redemption) Insert() error {
	var err error
	err = DB.Create(redemption).Error
	return err
}

func (redemption *Redemption) SelectUpdate() error {
	// This can update zero values
	return DB.Model(redemption).Select("redeemed_time", "status").Updates(redemption).Error
}

// Update Make sure your token's fields is completed, because this will update non-zero values
//
// product_type / product_id 刻意不在这份清单里:商品类型在建码那一刻就定死,与 key 同级。
// 允许改的话,一张已经发给用户的"送 10 美元"的码可以被悄悄改成"送年度套餐",
// 而码面上的字没有任何变化。要换商品就重新建一批。
func (redemption *Redemption) Update() error {
	var err error
	err = DB.Model(redemption).Select("name", "status", "quota", "redeemed_time", "expired_time").Updates(redemption).Error
	return err
}

func (redemption *Redemption) Delete() error {
	var err error
	err = DB.Delete(redemption).Error
	return err
}

func DeleteRedemptionById(id int) (err error) {
	if id == 0 {
		return errors.New("id 为空！")
	}
	redemption := Redemption{Id: id}
	err = DB.Where(redemption).First(&redemption).Error
	if err != nil {
		return err
	}
	return redemption.Delete()
}

// DeleteInvalidRedemptions 批量清掉已用 / 已禁用 / 已过期的码。
//
// creatorId 与 GetAllRedemptions / SearchRedemptions 同一口径(0 = 全量,
// 其余只作用于自己发的码)。它此前**没有**这个参数,于是分桶只挡住了读、
// 改、按 id 删,而这一条 role=10 一按就横扫全站 —— 包括超管发出去的、
// 已经被用户兑掉的那些行,而那些行正是"这张码到底给谁了"唯一的记录。
func DeleteInvalidRedemptions(creatorId int) (int64, error) {
	now := common.GetTimestamp()
	query := scopeRedemptionsToCreator(DB.Model(&Redemption{}), creatorId)
	result := query.Where("status IN ? OR (status = ? AND expired_time != 0 AND expired_time < ?)", []int{common.RedemptionCodeStatusUsed, common.RedemptionCodeStatusDisabled}, common.RedemptionCodeStatusEnabled, now).Delete(&Redemption{})
	return result.RowsAffected, result.Error
}
