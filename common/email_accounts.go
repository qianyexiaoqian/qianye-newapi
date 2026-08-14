package common

// email_accounts.go —— 多 SMTP 发件账号:账号表、发件模式、择号。
//
// ═══════════════════════════ 为什么要多账号 ═══════════════════════════
//
// 单个发件邮箱在一小时内发得太多,收件方(尤其 Gmail / Outlook)会开始把整批
// 邮件判成垃圾,而且**这件事对发件方是不可见的**:SMTP 依旧返回 250 成功,
// 邮件安安静静地进了垃圾箱。因此这里的核心不是"发得出去",而是把发送量摊到
// 多个账号上,并且给每个账号一个小时级的上限,让它在触顶之前就换号。
//
// ═══════════════════════════ 没配账号 = 逐位沿用旧行为 ═══════════════════════════
//
// 账号表为空时 Resolve 返回那份由 SMTPServer / SMTPAccount / SMTPToken … 这些
// 老全局变量拼出来的"隐式账号",SendEmail 的行为与本功能上线前一个字节都不差。
// 这是唯一能让"升级二进制但先不配多账号"的部署安全的形状 —— 而那正是绝大多数
// 部署在升级当天的状态。
//
// ═══════════════════════════ 用量统计为什么走 hook ═══════════════════════════
//
// 小时用量与发件台账都存在主库,而 model 包 import 了 common —— 反向依赖会成环。
// 因此这里只声明两个带 no-op 默认值的 hook 变量,实现体由 model 包在 init 里注入。
// 默认实现让 common 包在没有数据库的场景(单测、离线工具)照样可用:
// 用量恒为 0 ⇒ 小时上限永不触发 ⇒ 退化成"按模式轮着发",而不是一封都发不出去。

import (
	"fmt"
	"math/rand"
	"strings"
	"sync/atomic"
)

// 发件模式。
const (
	// SMTPSendModeFixed 固定用某一个账号(SMTPFixedAccountID)。
	SMTPSendModeFixed = "fixed"
	// SMTPSendModeRandom 每封随机挑一个。
	SMTPSendModeRandom = "random"
	// SMTPSendModeSequential 依次轮询。
	SMTPSendModeSequential = "sequential"
)

// SMTPSendMode 当前发件模式,默认依次。
//
// 默认选依次而不是固定:多账号这个功能存在的唯一理由就是把量摊开,
// 默认成固定等于配好了账号表却仍然只用一个,而管理端显示得好好的。
var SMTPSendMode = SMTPSendModeSequential

// SMTPFixedAccountID 固定模式下使用的账号 ID。
var SMTPFixedAccountID = ""

// SMTPAccountConfig 一个发件账号的完整配置。
//
// 字段与老的那组全局变量一一对应 —— 刻意不做精简:任何一项在某些服务商上
// 都是必需的(Outlook 要 force_auth_login,自签证书要 insecure_skip_verify),
// 少一项就会出现"这个账号在管理端配得出来、实际一封都发不了"。
type SMTPAccountConfig struct {
	// ID 是账号的稳定标识,发件台账与用量统计都按它归集。
	// 改名不影响历史统计,删号之后历史台账仍然查得到它发过什么。
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`

	Server   string `json:"server"`
	Port     int    `json:"port"`
	Account  string `json:"account"`
	Token    string `json:"token"`
	From     string `json:"from"`

	SSLEnabled         bool `json:"ssl_enabled"`
	StartTLSEnabled    bool `json:"start_tls_enabled"`
	InsecureSkipVerify bool `json:"insecure_skip_verify"`
	ForceAuthLogin     bool `json:"force_auth_login"`

	// HourlyLimit 是这个账号一小时内允许发出的封数,0 表示不限。
	//
	// 触顶的账号在择号时被跳过,而**不是**发送失败 —— 上限是为了避开对方的
	// 风控,不是为了拦住我们自己的邮件。全部账号都触顶时会回落成"照发",
	// 理由见 Resolve。
	HourlyLimit int `json:"hourly_limit"`
}

// FromAddress 返回这个账号实际的信封发件地址。
//
// 与老代码里那句 `if SMTPFrom == "" { SMTPFrom = SMTPAccount }` 同义,
// 但**不写回全局变量** —— 老代码那一句是有副作用的:它把一次发送的补齐结果
// 永久写进了进程级配置,于是管理端随后读到的 SMTPFrom 是一个从没被配过的值。
func (a SMTPAccountConfig) FromAddress() string {
	if a.From != "" {
		return a.From
	}
	return a.Account
}

// Valid 判定这个账号是否具备发件的最低条件。
func (a SMTPAccountConfig) Valid() bool {
	return a.Server != "" || a.Account != ""
}

// smtpAccounts 是账号表快照。整体替换,不做原地改写。
var smtpAccounts atomic.Pointer[[]SMTPAccountConfig]

// smtpRoundRobin 是依次模式的游标。
var smtpRoundRobin atomic.Uint64

// SMTPHourlyUsage 返回某个账号最近一小时已发出的封数。
//
// 由 model 包注入。默认恒 0 —— 见文件头。
var SMTPHourlyUsage = func(accountID string) int { return 0 }

// SMTPSendRecord 是一次发件尝试的台账。
type SMTPSendRecord struct {
	AccountID   string
	AccountName string
	From        string
	Receiver    string
	Subject     string
	Success     bool
	ErrorMsg    string
	DurationMs  int64
}

// SMTPRecordSend 落一条发件台账。由 model 包注入,默认丢弃。
//
// **绝不能让它的失败影响发件结果**:台账写不进去是运维问题,
// 而验证码发不出去是用户问题,后者严重得多。
var SMTPRecordSend = func(SMTPSendRecord) {}

// SMTPAccounts 返回当前账号表的副本。
func SMTPAccounts() []SMTPAccountConfig {
	p := smtpAccounts.Load()
	if p == nil {
		return nil
	}
	out := make([]SMTPAccountConfig, len(*p))
	copy(out, *p)
	return out
}

// SMTPAccounts2JSONString 序列化账号表,用于写回 options。
func SMTPAccounts2JSONString() string {
	accounts := SMTPAccounts()
	if accounts == nil {
		accounts = []SMTPAccountConfig{}
	}
	b, err := Marshal(accounts)
	if err != nil {
		SysLog("error marshalling smtp accounts: " + err.Error())
		return "[]"
	}
	return string(b)
}

// ValidateSMTPAccounts 校验待保存的账号表。
//
//nolint:gocyclo
//
// 在管理端保存时同步返回错误,而不是存下一份永远选不中任何账号的配置。
func ValidateSMTPAccounts(jsonStr string) error {
	accounts, err := parseSMTPAccounts(jsonStr)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(accounts))
	for i, a := range accounts {
		if a.ID == "" {
			return fmt.Errorf("第 %d 个 SMTP 账号缺少 id —— 发件台账与用量统计都按 id 归集,缺了就统计不到它", i+1)
		}
		if seen[a.ID] {
			return fmt.Errorf("SMTP 账号 id 重复: %q —— 两个账号的发件量会被记到同一本账上", a.ID)
		}
		seen[a.ID] = true
		if !a.Valid() {
			return fmt.Errorf("SMTP 账号 %q 既没有服务器也没有账号名,发不出任何邮件", a.ID)
		}
		if a.Port < 0 || a.Port > 65535 {
			return fmt.Errorf("SMTP 账号 %q 的端口 %d 非法", a.ID, a.Port)
		}
		if a.HourlyLimit < 0 {
			return fmt.Errorf("SMTP 账号 %q 的小时上限不能为负(0 表示不限)", a.ID)
		}
	}
	return nil
}

// ValidateSMTPSendMode 校验发件模式。
//
// 拼错时静默退回依次是不可接受的:运营选了「固定」却按轮询发,
// 发件人地址每封都在变,而设置页显示的是「固定」。
func ValidateSMTPSendMode(mode string) error {
	switch mode {
	case SMTPSendModeFixed, SMTPSendModeRandom, SMTPSendModeSequential:
		return nil
	default:
		return fmt.Errorf("SMTP 发件模式 %q 非法(可选 %s|%s|%s)",
			mode, SMTPSendModeFixed, SMTPSendModeRandom, SMTPSendModeSequential)
	}
}

// UpdateSMTPAccountsByJSONString 覆盖整张账号表。
func UpdateSMTPAccountsByJSONString(jsonStr string) error {
	accounts, err := parseSMTPAccounts(jsonStr)
	if err != nil {
		return err
	}
	smtpAccounts.Store(&accounts)
	return nil
}

func parseSMTPAccounts(jsonStr string) ([]SMTPAccountConfig, error) {
	trimmed := strings.TrimSpace(jsonStr)
	if trimmed == "" {
		return []SMTPAccountConfig{}, nil
	}
	var accounts []SMTPAccountConfig
	if err := UnmarshalJsonStr(trimmed, &accounts); err != nil {
		return nil, fmt.Errorf("SMTP 账号表不是合法的 JSON 数组: %w", err)
	}
	return accounts, nil
}

// legacySMTPAccount 把那组老全局变量包成一个隐式账号。
//
// ID 固定为 "legacy",于是"升级前就在发的那些邮件"和"升级后没配账号表时发的"
// 在台账里归到同一本账上,统计不会因为一次升级断成两截。
func legacySMTPAccount() SMTPAccountConfig {
	return SMTPAccountConfig{
		ID:                 "legacy",
		Name:               "默认(单账号配置)",
		Enabled:            true,
		Server:             SMTPServer,
		Port:               SMTPPort,
		Account:            SMTPAccount,
		Token:              SMTPToken,
		From:               SMTPFrom,
		SSLEnabled:         SMTPSSLEnabled,
		StartTLSEnabled:    SMTPStartTLSEnabled,
		InsecureSkipVerify: SMTPInsecureSkipVerify,
		ForceAuthLogin:     SMTPForceAuthLogin,
	}
}

// ResolveSMTPAccount 按当前模式挑一个账号。
//
// ── 择号顺序 ──
//
//  1. 账号表为空 → 老全局变量拼出的隐式账号(逐位沿用旧行为)
//  2. 过滤:enabled 且配置有效
//  3. 过滤:最近一小时用量未触顶(HourlyLimit > 0 时)
//  4. 按模式在候选里挑一个
//
// ── 全部触顶时为什么是"照发"而不是"报错" ──
//
// 小时上限的目的是避开对方风控,不是给自己加一道闸。全部触顶时如果拒发,
// 用户就收不到验证码 —— 那是一个确定的、当场可见的故障;而超额发送的代价是
// 概率性地进垃圾箱。两害相权,照发,并打一条告警让运维知道该加账号了。
// 这一条必须是显式决定,否则下一个人会"顺手"把它改成返回 error。
func ResolveSMTPAccount() (SMTPAccountConfig, error) {
	all := SMTPAccounts()
	if len(all) == 0 {
		legacy := legacySMTPAccount()
		if !legacy.Valid() {
			return SMTPAccountConfig{}, fmt.Errorf("SMTP 服务器未配置")
		}
		return legacy, nil
	}

	usable := make([]SMTPAccountConfig, 0, len(all))
	for _, a := range all {
		if a.Enabled && a.Valid() {
			usable = append(usable, a)
		}
	}
	if len(usable) == 0 {
		return SMTPAccountConfig{}, fmt.Errorf("没有可用的 SMTP 发件账号(账号表里全部被停用或配置不完整)")
	}

	// 固定模式在这一步就定下来:它的语义是"就用这个",不参与小时上限的跳过 ——
	// 明确指定了还被换掉,发件人地址就会与运营的预期不一致。
	if SMTPSendMode == SMTPSendModeFixed {
		for _, a := range usable {
			if a.ID == SMTPFixedAccountID {
				return a, nil
			}
		}
		// 指定的账号被停用/删掉了。回落到第一个可用账号并告警 ——
		// 静默回落会让"我明明固定了 A"和"实际在用 B"永远对不上。
		SysError(fmt.Sprintf("SMTP: 固定发件账号 %q 不在可用账号里,本次回落到 %q",
			SMTPFixedAccountID, usable[0].ID))
		return usable[0], nil
	}

	eligible := make([]SMTPAccountConfig, 0, len(usable))
	for _, a := range usable {
		if a.HourlyLimit > 0 && SMTPHourlyUsage(a.ID) >= a.HourlyLimit {
			continue
		}
		eligible = append(eligible, a)
	}
	if len(eligible) == 0 {
		SysError("SMTP: 全部发件账号都已达到小时上限,本次仍按轮询照常发送 —— " +
			"继续这样下去会开始进垃圾箱,请增加账号或调高上限")
		eligible = usable
	}

	if SMTPSendMode == SMTPSendModeRandom {
		return eligible[rand.Intn(len(eligible))], nil
	}
	// 依次:全局游标取模。多节点各有各的游标,但每个节点内部仍是均匀轮询,
	// 合起来对每个账号的期望份额一致 —— 这个场景不需要跨节点严格轮转。
	n := smtpRoundRobin.Add(1) - 1
	return eligible[n%uint64(len(eligible))], nil
}
