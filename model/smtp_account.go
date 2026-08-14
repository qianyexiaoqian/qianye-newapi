package model

// smtp_account.go —— SMTP 发件账号表。
//
// ═══════════════════════ 为什么是一张表,而不是 options 里的一块 JSON ═══════════════════════
//
// 账号表是**会被反复单条编辑**的数据(加一个号、停用一个号、改一个号的小时上限),
// 而 options 里的 JSON 只能整块读写。整块写有两个后果:
//
//   1. 两个管理员同屏各改一个账号,后保存的把先保存的整块覆盖掉,而界面上
//      两次保存都提示成功。
//   2. 想单独停用一个号,也要把**全部**账号(含密码)重新序列化写回去。
//
// 一行一个账号之后,这两件事都不再可能:单条 UPDATE 只碰那一行。
//
// ═══════════════════════ 发件时直接查库,不做内存快照 ═══════════════════════
//
// 与分组倍率、可选清单那些**跑在 relay 热路径上**的配置不同,发信是低频动作
// (验证码、找回密码、通知),一次带索引的查询完全承受得起。因此这里不做快照:
// 没有快照就没有"改了配置多久生效"、没有多节点陈旧、没有预热失败的降级态,
// 而那三样是本仓其余快照模块里最难排查的一类问题。
//
// 判据很简单:如果哪天发信变成了热路径(比如群发),再加缓存也不迟;
// 反过来"先加了快照再发现根本不需要"是不可逆的复杂度。

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
)

// SmtpAccount 是一个 SMTP 发件账号。
//
// 字段与 common.SMTPAccountConfig 一一对应 —— 后者是发件侧的值对象,
// 本结构是它的持久化形态。刻意不复用同一个结构:GORM 标签、主键、时间戳
// 属于存储关注点,而 common 包不该知道数据库的存在(它被 model 依赖,反向会成环)。
type SmtpAccount struct {
	Id int `json:"id" gorm:"primaryKey;autoIncrement"`
	// AccountId 是**发件台账与用量统计的归集键**,一旦用过就不该再改。
	//
	// 不直接拿自增主键当归集键:账号被删掉重建之后主键会变,而历史台账里
	// 那些行仍然指着旧值 —— 统计会莫名其妙断成两截。AccountId 由前端生成
	// (与账号名无关的随机串),删号重建时运营可以有意沿用同一个,也可以换新的。
	AccountId string `json:"account_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	Name      string `json:"name" gorm:"type:varchar(128)"`
	Enabled   bool   `json:"enabled"`

	Server   string `json:"server" gorm:"type:varchar(255)"`
	Port     int    `json:"port"`
	Account  string `json:"account" gorm:"type:varchar(255)"`
	Token    string `json:"token" gorm:"type:varchar(512)"`
	FromAddr string `json:"from_addr" gorm:"type:varchar(255)"`

	SSLEnabled         bool `json:"ssl_enabled"`
	StartTLSEnabled    bool `json:"start_tls_enabled"`
	InsecureSkipVerify bool `json:"insecure_skip_verify"`
	ForceAuthLogin     bool `json:"force_auth_login"`

	// HourlyLimit 是这个账号一小时内允许发出的封数,0 表示不限。
	HourlyLimit int `json:"hourly_limit"`
	// SortOrder 决定「依次」模式的轮转顺序,小的在前。
	SortOrder int `json:"sort_order"`

	CreatedAt int64 `json:"created_at" gorm:"bigint"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint"`
}

// toConfig 把持久化行转成发件侧的值对象。
func (a *SmtpAccount) toConfig() common.SMTPAccountConfig {
	return common.SMTPAccountConfig{
		ID:                 a.AccountId,
		Name:               a.Name,
		Enabled:            a.Enabled,
		Server:             a.Server,
		Port:               a.Port,
		Account:            a.Account,
		Token:              a.Token,
		From:               a.FromAddr,
		SSLEnabled:         a.SSLEnabled,
		StartTLSEnabled:    a.StartTLSEnabled,
		InsecureSkipVerify: a.InsecureSkipVerify,
		ForceAuthLogin:     a.ForceAuthLogin,
		HourlyLimit:        a.HourlyLimit,
	}
}

// init 把账号读取接到 common 的 provider hook 上。
//
// 走 hook 而不是让 common 直接查库:model import common,反向依赖会成环。
func init() {
	common.SMTPAccountsProvider = LoadSmtpAccountsForSending
}

// LoadSmtpAccountsForSending 是 common.SMTPAccountsProvider 的实现体。
//
// 按 sort_order 再按 id 排序,让「依次」模式的轮转顺序对运营是可预期的
// (仅按 id 的话,删掉中间一个再加回来,顺序就变了,而界面上看不出来)。
//
// 查不到 / 查失败一律返回 nil ⇒ 调用方按「没有配置任何账号」处理。
// 失败时**不**返回半份清单:半份清单会让择号在一个运营从没配过的子集里轮转。
func LoadSmtpAccountsForSending() []common.SMTPAccountConfig {
	if DB == nil {
		return nil
	}
	var rows []*SmtpAccount
	if err := DB.Order("sort_order asc, id asc").Find(&rows).Error; err != nil {
		common.SysError("failed to load smtp accounts: " + err.Error())
		return nil
	}
	out := make([]common.SMTPAccountConfig, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toConfig())
	}
	return out
}

// GetAllSmtpAccounts 返回全部账号,给管理端列表用。
func GetAllSmtpAccounts() ([]*SmtpAccount, error) {
	var rows []*SmtpAccount
	err := DB.Order("sort_order asc, id asc").Find(&rows).Error
	return rows, err
}

// validateSmtpAccount 是写入侧的结构校验。
//
// 与 common.ValidateSMTPAccounts 同一套判据,但作用在单条上 —— 单条增删改
// 之后不再有"整表校验"这个时机,所以判据必须搬到每一次写入上。
func validateSmtpAccount(a *SmtpAccount) error {
	if a.AccountId == "" {
		return fmt.Errorf("account_id 不能为空 —— 发件台账与用量统计都按它归集")
	}
	if a.Server == "" && a.Account == "" {
		return fmt.Errorf("既没有服务器也没有账号名,发不出任何邮件")
	}
	if a.Port < 0 || a.Port > 65535 {
		return fmt.Errorf("端口 %d 非法", a.Port)
	}
	if a.HourlyLimit < 0 {
		return fmt.Errorf("小时上限不能为负(0 表示不限)")
	}
	return nil
}

func CreateSmtpAccount(a *SmtpAccount) error {
	if err := validateSmtpAccount(a); err != nil {
		return err
	}
	now := common.GetTimestamp()
	a.Id = 0
	a.CreatedAt = now
	a.UpdatedAt = now
	return DB.Create(a).Error
}

// UpdateSmtpAccount 按主键更新一条账号。
//
// token 为空串时**保留库里原值**:管理端列表不回显密码(回显等于把全部发件
// 凭据发给每一个打开设置页的管理员),因此"没填密码"必须表示"不改密码",
// 而不是"把密码清空" —— 后者的表现是运营改了个备注名,这个号第二天开始
// 认证失败。这条与上游单账号表单里那句「留空以保留现有凭证」是同一个语义。
func UpdateSmtpAccount(a *SmtpAccount) error {
	if err := validateSmtpAccount(a); err != nil {
		return err
	}
	var existing SmtpAccount
	if err := DB.First(&existing, a.Id).Error; err != nil {
		return err
	}
	if a.Token == "" {
		a.Token = existing.Token
	}
	a.CreatedAt = existing.CreatedAt
	a.UpdatedAt = common.GetTimestamp()
	// Select 明确列出可写字段:用 Save/Updates(struct) 时 GORM 会跳过零值,
	// 于是"把 enabled 关掉""把小时上限改回 0(不限)"这两个动作都会静默失效。
	return DB.Model(&SmtpAccount{}).Where("id = ?", a.Id).Select(
		"account_id", "name", "enabled", "server", "port", "account", "token",
		"from_addr", "ssl_enabled", "start_tls_enabled", "insecure_skip_verify",
		"force_auth_login", "hourly_limit", "sort_order", "updated_at",
	).Updates(a).Error
}

func DeleteSmtpAccount(id int) error {
	return DB.Delete(&SmtpAccount{}, id).Error
}

// MigrateLegacySMTPAccount 把老的单账号 SMTP 配置一次性导入成账号表的一条。
//
// ═══════════════════════ 幂等判据:AccountId = "legacy" 这一行存不存在 ═══════════════════════
//
// 不用"表是不是空的"当判据:运营完全可能先加了新账号、再重启,那时表非空,
// 老配置就永远迁不进来,而设置页上老表单已经被移除 —— 那套凭据会变成一份
// 谁都看不见、也删不掉的死数据。
//
// 也不用"迁移标记 option"当判据:多加一个状态就多一个会与事实不符的地方,
// 而"那一行在不在"本身就是自描述的。
//
// 老配置为空(从没配过 SMTP)时什么都不做 —— 导入一个发不出信的空账号,
// 只会让择号在一个必然失败的候选里轮转。
func MigrateLegacySMTPAccount() error {
	if DB == nil {
		return nil
	}
	legacy := common.LegacySMTPAccount()
	if !legacy.Valid() {
		return nil
	}
	var count int64
	if err := DB.Model(&SmtpAccount{}).Where("account_id = ?", legacy.ID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	row := &SmtpAccount{
		AccountId:          legacy.ID,
		Name:               legacy.Name,
		Enabled:            true,
		Server:             legacy.Server,
		Port:               legacy.Port,
		Account:            legacy.Account,
		Token:              legacy.Token,
		FromAddr:           legacy.From,
		SSLEnabled:         legacy.SSLEnabled,
		StartTLSEnabled:    legacy.StartTLSEnabled,
		InsecureSkipVerify: legacy.InsecureSkipVerify,
		ForceAuthLogin:     legacy.ForceAuthLogin,
		SortOrder:          0,
	}
	if err := CreateSmtpAccount(row); err != nil {
		return err
	}
	common.SysLog(fmt.Sprintf("SMTP: 已把原单账号配置(%s)迁移成发件账号表的第一条", legacy.Server))
	return nil
}
