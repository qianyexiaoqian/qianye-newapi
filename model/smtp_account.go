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
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
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
	// 服务器与账号**都必须填**,不是"填一个就行"。
	//
	// 早先判的是 `Server == "" && Account == ""`(两个都空才拒),于是只填账号名、
	// 漏填服务器的号能保存成功、enabled=true、并进入择号轮转 —— 每被轮到一次就
	// 失败一次,而设置页上它看起来完全正常。依次/随机模式下的表现是
	// 「固定比例的验证码发不出去」,而且没有任何一处会指出是哪个号的问题。
	//
	// 项目方原话:「smtp账号和服务器信息要保存完整,因为随时可能是其他服务器
	// 来发信,不填写怎么写」。
	if a.Server == "" {
		return fmt.Errorf("必须填写 SMTP 服务器 —— 随时可能换别的服务器发信,不填就发不出去")
	}
	// 账号刻意**不**强制:有些 SMTP 服务器不要求认证(内网中继),
	// 那种配置的账号与密码天然是空的,发件路径已有不认证的分支。
	// 真正不可缺的是服务器 —— 没有它连都连不上。
	if a.Port <= 0 {
		return fmt.Errorf("必须填写端口(常用 25 / 465 / 587)")
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

// retiredSMTPAccountsOptionKey 是中途那一版「多账号存成一整块 JSON」留下的 option 键。
//
// 它的值是一个账号数组,**每个元素都带明文密码**。账号改存独立表之后它一个读取方
// 都没有了,但那一行还在 options 表里,而 loadOptionsFromDatabase 会把库里的**任何**
// 键装进 common.OptionMap,GetOptions 随后按后缀判敏感 —— "SMTPAccounts" 不带任何
// 敏感后缀,于是整块含密码的 JSON 原样下发给了设置页。
const retiredSMTPAccountsOptionKey = "SMTPAccounts"

// retiredSMTPOptionKeys 是 SMTP 配置在 options 表里占过、而现在一个读取方都没有的全部键。
//
// 前九个是最早那版单账号表单(它们唯一的用途是被 LegacySMTPAccount 读一次、
// 迁进账号表),最后一个是上面那块多账号 JSON。发件路径只认 smtp_accounts 表,
// 这些键留在库里既是一份会与事实不符的影子配置,也是一份没人看管的明文凭据。
var retiredSMTPOptionKeys = []string{
	"SMTPServer",
	"SMTPPort",
	"SMTPAccount",
	"SMTPToken",
	"SMTPFrom",
	"SMTPSSLEnabled",
	"SMTPStartTLSEnabled",
	"SMTPInsecureSkipVerify",
	"SMTPForceAuthLogin",
	retiredSMTPAccountsOptionKey,
}

// MigrateLegacySMTPAccount 把老的单账号 SMTP 配置一次性导入成账号表的一条,
// 并在同一个事务里把 options 表里那组已经退役的 SMTP 键**消费掉**。
//
// ═══════════════════════ 幂等判据:老配置那几行还在不在 ═══════════════════════
//
// 判据**不能**是"账号表里有没有 account_id = legacy 这一行":那一行是可以被运营
// 在界面上删掉的,而删掉之后判据就又变成了"没迁过",于是下次重启原样插回去 ——
// 表现是这个号「删不掉」,重启一次复活一次,而它带着一套运营已经决定弃用的凭据
// 继续参与轮转发信。判据必须能区分「还没迁过」与「迁过了但被人删了」,
// 而"那一行在不在"回答不了第二个问题。
//
// 改成消费源:迁移成功的同时把老配置那几行删掉,于是
//
//	源还在  ⇒ 还没迁过   ⇒ 迁
//	源没了  ⇒ 迁过了     ⇒ 不迁(账号被删掉也不再复活)
//
// 这与本仓 migrateLegacyOption(退役前端配置)是同一套形状:迁完即删源。
// 它也不引入"迁移标记"那种额外状态 —— 判据仍然是自描述的,只是换了个自描述的东西。
//
// 首次升级的老站点照样自动迁得过来:那时源还在,行为与改动前逐位一致。
//
// 老配置为空(从没配过 SMTP)时不导入账号 —— 导入一个发不出信的空账号,
// 只会让择号在一个必然失败的候选里轮转 —— 但那几行仍然要清掉。
func MigrateLegacySMTPAccount() error {
	if DB == nil {
		return nil
	}
	purged := false
	// 日志一律等事务提交之后再发:在事务里发的话,一次晚到的回滚会留下一条
	// 「已迁移」,而 main.go 那边同时打出「迁移失败」—— 排障的人只能二选一地猜。
	var commitLogs []string
	var commitWarning string
	err := DB.Transaction(func(tx *gorm.DB) error {
		var source []Option
		if err := tx.Where(map[string]any{"key": retiredSMTPOptionKeys}).Find(&source).Error; err != nil {
			return err
		}
		if len(source) == 0 {
			return nil
		}
		values := make(map[string]string, len(source))
		for _, option := range source {
			values[option.Key] = option.Value
		}

		// 「以前配过吗」问的是**库里那几行**,而不是进程里的全局变量。两者本该一致
		// (全局变量正是 InitOptionMap 从这些行装载来的),真不一致时以库为准:
		// 否则一次装载顺序上的意外会让这里把"还没读进来的配置"当成"没配过",
		// 而下一步是删源 —— 那一步不可逆。迁移用 Configured(填了一半也算配过);
		// 能不能发信由 Valid 在择号时判。
		legacySource := common.SMTPAccountConfig{Server: values["SMTPServer"], Account: values["SMTPAccount"]}
		if legacySource.Configured() {
			legacy := common.LegacySMTPAccount()
			var existing int64
			if err := tx.Model(&SmtpAccount{}).Where("account_id = ?", legacy.ID).Count(&existing).Error; err != nil {
				return err
			}
			// 已经有 legacy 那一行(上一版代码迁过、但没消费源)时只消费源,不重复插入。
			if existing == 0 {
				now := common.GetTimestamp()
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
					CreatedAt:          now,
					UpdatedAt:          now,
				}
				// 校验不过就让整个事务回滚:源没被消费,下次启动还能再试,
				// 而不是把一份迁不进来的配置直接删掉。
				if err := validateSmtpAccount(row); err != nil {
					return err
				}
				if err := tx.Create(row).Error; err != nil {
					return err
				}
				commitLogs = append(commitLogs,
					fmt.Sprintf("SMTP: 已把原单账号配置(%s)迁移成发件账号表的第一条", legacy.Server))
			}
		}

		// 多账号 JSON 不自动导入账号表:那份 JSON 里的条目带着 enabled,导进来就会
		// 直接进入轮转,而它从来没经过写入侧校验(本仓演示库里那一条的 server 就是 "8")
		// —— 一个连不上的号进轮转的表现是「固定比例的验证码发不出去」。
		// 因此这里只把它们**报出来**,由运营在「SMTP 发件账号」里决定要不要重建。
		if raw := strings.TrimSpace(values[retiredSMTPAccountsOptionKey]); raw != "" {
			var stranded []common.SMTPAccountConfig
			if err := common.Unmarshal([]byte(raw), &stranded); err != nil {
				commitWarning = "SMTP: 退役的 SMTPAccounts 配置解析失败,已按原样删除: " + err.Error()
			} else if len(stranded) > 0 {
				labels := make([]string, 0, len(stranded))
				for _, account := range stranded {
					labels = append(labels, fmt.Sprintf("%s(%s / %s)", account.ID, account.Name, account.Server))
				}
				commitLogs = append(commitLogs,
					"SMTP: 已删除退役的 SMTPAccounts 配置(那块 JSON 里存着明文密码,而发件早已只读账号表)。"+
						"下列账号只存在于这个键里,如仍需使用请在「SMTP 发件账号」里重新添加(密码要重新填): "+
						strings.Join(labels, ", "))
			}
		}

		if err := tx.Where(map[string]any{"key": retiredSMTPOptionKeys}).Delete(&Option{}).Error; err != nil {
			return err
		}
		purged = true
		return nil
	})
	if err != nil {
		return err
	}
	for _, line := range commitLogs {
		common.SysLog(line)
	}
	if commitWarning != "" {
		common.SysError(commitWarning)
	}
	if purged {
		// SMTPAccounts 不在 InitOptionMap 的种子里 —— 它进内存的唯一途径就是那条孤儿行。
		// 删掉之后 OptionMap 回到「从没见过这个键」的形状,本进程也不必等重启。
		// 其余几个键 InitOptionMap 会以空值种下,留在图里不含凭据。
		common.OptionMapRWMutex.Lock()
		delete(common.OptionMap, retiredSMTPAccountsOptionKey)
		common.OptionMapRWMutex.Unlock()
	}
	return nil
}
