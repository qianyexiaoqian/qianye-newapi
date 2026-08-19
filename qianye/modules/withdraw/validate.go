package withdraw

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
)

// 收款渠道。
//
// 白名单写死在代码里而不是配置里:每个渠道都对应一组必填字段与一套脱敏规则,
// 配置里加一个 "paypal" 而代码不认识它,结果是收款信息被原样存成空脱敏串。
// 能配置的东西必须是代码已经实现的东西。
const (
	ChannelAlipay = "alipay"
	ChannelWechat = "wechat"
	ChannelBank   = "bank"
	ChannelUSDT   = "usdt_trc20"
	ChannelPaypal = "paypal"
)

// payeeField 描述收款信息里的一个字段。
// MinRunes/MaxRunes 按字符计:银行卡号是数字,但姓名是中文,两者不能用字节长度。
type payeeField struct {
	Key      string
	Required bool
	MinRunes int
	MaxRunes int
}

// payeeSpecs 是各渠道的字段规格。未列出的键会被丢弃 ——
// 前端多传一个字段就多一份 PII 被加密留存,这不是善意的宽容。
var payeeSpecs = map[string][]payeeField{
	ChannelAlipay: {
		{Key: "real_name", Required: true, MinRunes: 1, MaxRunes: 64},
		{Key: "account", Required: true, MinRunes: 4, MaxRunes: 64},
	},
	ChannelWechat: {
		{Key: "real_name", Required: true, MinRunes: 1, MaxRunes: 64},
		{Key: "account", Required: true, MinRunes: 4, MaxRunes: 64},
	},
	ChannelBank: {
		{Key: "real_name", Required: true, MinRunes: 1, MaxRunes: 64},
		{Key: "bank_name", Required: true, MinRunes: 2, MaxRunes: 64},
		{Key: "account_no", Required: true, MinRunes: 6, MaxRunes: 32},
		{Key: "branch", Required: false, MinRunes: 0, MaxRunes: 64},
	},
	ChannelUSDT: {
		{Key: "address", Required: true, MinRunes: 26, MaxRunes: 64},
	},
	ChannelPaypal: {
		{Key: "email", Required: true, MinRunes: 5, MaxRunes: 128},
	},
}

// payeeAccountKey 指出每个渠道里"钱最终去哪"的那**一个**字段。
//
// 风控指纹只摘要这一个字段(见 payeeDigest):其余字段是收款人的自述,
// 同一张卡上它们可以每次都写得不一样,而钱去的仍是同一个账号。
// 新增渠道时必须同时在这里登记,否则该渠道的指纹会回落到整条记录的旧口径,
// 跨账号信号对它静默失效。
var payeeAccountKey = map[string]string{
	ChannelAlipay: "account",
	ChannelWechat: "account",
	ChannelBank:   "account_no",
	ChannelUSDT:   "address",
	ChannelPaypal: "email",
}

// normalizePayeeAccount 按渠道归一账号字段,让"同一个账号的不同写法"摘出同一个指纹。
//
// 空白一律去掉(含全角空格):卡号写成 "6225 8801 3762 4156" 与写成一整串
// 是同一张卡,而 acceptPayee 只做首尾 TrimSpace,中间的空格原样留着。
//
// 大小写按渠道分开处理,不能一刀切:
//   - paypal 的 email、alipay/wechat 的账号(手机号或邮箱)问的是"同一个信箱/
//     同一个账号",邮箱域名大小写不敏感,Mule@Example.com 与 mule@example.com
//     是同一个收款目的地,必须折成小写;
//   - bank 的账号可能是 IBAN(字母数字混排,书写惯例是大写并用空格/连字符分组),
//     折成大写并去掉连字符;
//   - usdt_trc20 的地址是 Base58,大小写本身就是地址的一部分,折叠会把两个
//     不同的钱包地址并成一个 —— 这里只去空白,一个字符都不动。
func normalizePayeeAccount(channel, raw string) string {
	s := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, raw)
	switch channel {
	case ChannelBank:
		return strings.ToUpper(strings.ReplaceAll(s, "-", ""))
	case ChannelUSDT:
		return s
	default:
		return strings.ToLower(s)
	}
}

// SupportedChannels 返回全部收款渠道标识,供前端渲染选择器。
func SupportedChannels() []string {
	return []string{ChannelAlipay, ChannelWechat, ChannelBank, ChannelUSDT, ChannelPaypal}
}

// createRequest 是提交提现申请的请求体。
//
// Quota 一律是整数额度,后端绝不接受前端传的法币金额:法币是派生量,
// 由冻结汇率单向换算而来(见 pricing.go)。
type createRequest struct {
	// ClientRequestId 由前端在打开申请弹窗时生成并缓存,重试沿用同一个。
	// 点击时才生成会让每次重试变成一笔新提现,幂等彻底失效(裁定 C10)。
	ClientRequestId string `json:"client_request_id"`

	Method string `json:"method"`
	Quota  int64  `json:"quota"`
	Remark string `json:"remark"`

	// PayeeRef 复用已保存的收款方式,与 Payee 二选一。
	PayeeRef     string            `json:"payee_ref"`
	PayeeChannel string            `json:"payee_channel"`
	Payee        map[string]string `json:"payee"`

	// ProofRef 是先经 POST /withdraw/proofs 上传得到的凭证标识,可选。
	// 图片本体从不随本请求走 —— JSON 里塞 base64 会让整条申请链路(含幂等重放)
	// 拖着几 MiB 的负载,而这条路径上真正稀缺的是事务时间。
	ProofRef string `json:"proof_ref"`

	// PayPassword 是资金二次确认。它**不进** acceptedRequest,也永远不落库:
	// 唯一的消费方是 handleCreate 里那一次 paypass.Require,用完即弃。
	// 让它随请求体走而不是走 paypass.HeaderPayPassword,是因为申请本来就是
	// 一次 JSON 提交,多一个请求头只会让前端在两处维护同一个值。
	PayPassword string `json:"pay_password"`
}

// acceptedRequest 是通过受理校验后的规范化请求。
type acceptedRequest struct {
	IdemKey string
	Method  string
	Quota   int64
	Remark  string

	PayeeRef     string
	PayeeChannel string
	Payee        map[string]string
	ProofRef     string
}

// acceptCreate 做全部与数据库无关的校验并规范化入参。
//
// 前端的每一条校验后端都要再做一遍:前端校验只是体验,绕过它只需要一个 curl。
func acceptCreate(req createRequest, cfg config.Withdraw) (acceptedRequest, error) {
	idem := strings.TrimSpace(req.ClientRequestId)
	if len(idem) < 8 || len(idem) > 64 {
		return acceptedRequest{}, errIdemKeyRequired
	}

	method := strings.TrimSpace(req.Method)
	if !cfg.HasWithdrawMethod(method) {
		return acceptedRequest{}, errMethodNotAllowed
	}

	if req.Quota <= 0 || req.Quota > int64(common.MaxQuota) {
		return acceptedRequest{}, errAmountOutOfRange
	}
	// 单笔上限必须在这里拦。只靠 common.MaxQuota 的话,上界就是主库 int32 容量:
	// 一个长期累积的邀请人可以一次申请 20 亿额度,把整个佣金池冻在一张单上。
	// 申请即冻结不构成超发,但它会让运营在一张单面前直接失去处置空间。
	if cfg.MaxQuotaPerOrder > 0 && req.Quota > cfg.MaxQuotaPerOrder {
		return acceptedRequest{}, errAmountOutOfRange
	}
	if req.Quota < cfg.MinQuota {
		return acceptedRequest{}, errAmountTooSmall
	}

	remark, err := checkRunes(req.Remark, cfg.RemarkMaxRunes)
	if err != nil {
		return acceptedRequest{}, errRemarkTooLong
	}

	acc := acceptedRequest{
		IdemKey: idem,
		Method:  method,
		Quota:   req.Quota,
		Remark:  remark,
	}
	if method != config.WithdrawMethodFiat {
		// 凭证只服务于法币打款。quota 单带着 proof_ref 提交必须报错而不是静默忽略:
		// 静默忽略的话那张图会永远停在"未绑定"状态,用户以为交了、审核看不到,
		// 而它最终被孤儿清理悄悄删掉。
		if strings.TrimSpace(req.ProofRef) != "" {
			return acceptedRequest{}, errProofDisabled
		}
		return acc, nil
	}
	acc.ProofRef, err = acceptProofRef(req.ProofRef, cfg)
	if err != nil {
		return acceptedRequest{}, err
	}

	// 法币路径必须有收款信息:要么引用已保存的,要么本次现填。
	if ref := strings.TrimSpace(req.PayeeRef); ref != "" {
		acc.PayeeRef = ref
		return acc, nil
	}
	channel, payee, err := acceptPayee(req.PayeeChannel, req.Payee)
	if err != nil {
		return acceptedRequest{}, err
	}
	acc.PayeeChannel, acc.Payee = channel, payee
	return acc, nil
}

// acceptProofRef 校验可选的凭证标识。
//
// 长度上界不是形式主义:ref 会直接进 WHERE 条件,不设上界意味着一个 1 MiB 的
// 字符串会被原样发给数据库比较。空串是合法的 —— 需求原文写的就是"(可选)"。
func acceptProofRef(raw string, cfg config.Withdraw) (string, error) {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		return "", nil
	}
	if !cfg.ProofOn() {
		return "", errProofDisabled
	}
	if len(ref) > 64 {
		return "", errProofNotFound
	}
	return ref, nil
}

// acceptPayee 校验并规范化收款信息。
//
// 返回的 map 只含规格里声明过的键,顺序无关(指纹由 canonicalPayee* 的排序/取字段保证稳定)。
func acceptPayee(rawChannel string, raw map[string]string) (string, map[string]string, error) {
	channel := strings.TrimSpace(rawChannel)
	spec, ok := payeeSpecs[channel]
	if !ok {
		return "", nil, errPayeeInvalid
	}
	if len(raw) == 0 {
		return "", nil, errPayeeRequired
	}

	out := make(map[string]string, len(spec))
	for _, f := range spec {
		v := strings.TrimSpace(raw[f.Key])
		if v == "" {
			if f.Required {
				return "", nil, errPayeeRequired
			}
			continue
		}
		if n := utf8.RuneCountInString(v); n < f.MinRunes || n > f.MaxRunes {
			return "", nil, errPayeeInvalid
		}
		// 控制字符会破坏 canonicalPayee* 的分隔符语义,也可能是注入尝试。
		if strings.ContainsAny(v, "\x00\x1e\x1f\n\r") {
			return "", nil, errPayeeInvalid
		}
		out[f.Key] = v
	}
	return channel, out, nil
}

// checkRunes 按字符(rune)而非字节校验长度。
//
// 需求写的是"200 字",不是"200 字节"。用 len() 的话一个中文占 3 字节、
// 一个 emoji 占 4 字节,用户写 67 个汉字就会被拒 —— 这是最容易被漏掉的坑。
//
// 先用字节数做一次廉价上界剪枝:rune 最多 4 字节,超过 4*max 字节必然超限,
// 不必对一个超大请求体做完整遍历。
func checkRunes(raw string, max int) (string, error) {
	s := strings.TrimSpace(raw)
	if max <= 0 {
		max = 200
	}
	if len(s) > max*4 {
		return "", errRemarkTooLong
	}
	if utf8.RuneCountInString(s) > max {
		return "", errRemarkTooLong
	}
	return s, nil
}
