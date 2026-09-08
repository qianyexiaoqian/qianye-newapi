package operation_setting

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

// TopUpExternalLinksOptionKey 是站外跳转入口在 options 表里的键。
const TopUpExternalLinksOptionKey = "payment_setting.external_links"

// TopUpExternalLink 是一条只负责「把用户送走」的充值入口。
//
// 它排在支付通道旁边、长得也一样，但后端不为它建订单、不收回调、不加额度：
// 站点自己不接支付通道、只在站外卖固定金额的兑换码时，用户得有个地方点过去。
//
// Amount 把这条链接绑在某个充值金额上（通常是 AmountOptions 里的一档）：
// 用户选中那个金额时它才出现，选别的金额就只剩支付通道。没有「不绑金额、
// 一直显示」这一档 —— 站外卖的是固定面额的兑换码，链接与面额本来就是一对一的。
type TopUpExternalLink struct {
	Amount int    `json:"amount"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Icon   string `json:"icon,omitempty"`
}

type PaymentSetting struct {
	AmountOptions  []int           `json:"amount_options"`
	AmountDiscount map[int]float64 `json:"amount_discount"` // 充值金额对应的折扣，例如 100 元 0.9 表示 100 元充值享受 9 折优惠

	ExternalLinks []TopUpExternalLink `json:"external_links"` // 展示在支付方式里的站外跳转入口，通常指向自建商店的某个兑换码金额

	ComplianceConfirmed    bool   `json:"compliance_confirmed"`
	ComplianceTermsVersion string `json:"compliance_terms_version"`
	ComplianceConfirmedAt  int64  `json:"compliance_confirmed_at"`
	ComplianceConfirmedBy  int    `json:"compliance_confirmed_by"`
	ComplianceConfirmedIP  string `json:"compliance_confirmed_ip"`
}

const CurrentComplianceTermsVersion = "v1"

// 默认配置
var paymentSetting = PaymentSetting{
	AmountOptions:  []int{10, 20, 50, 100, 200, 500},
	AmountDiscount: map[int]float64{},
	ExternalLinks:  []TopUpExternalLink{},
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("payment_setting", &paymentSetting)
}

func GetPaymentSetting() *PaymentSetting {
	return &paymentSetting
}

// GetTopUpExternalLinks 返回可以直接渲染成按钮的外链，顺手把配坏的条目丢掉。
//
// 这里只放行 http/https：管理员填进来的地址会原样变成用户端的 href，
// javascript: 之类的伪协议落到那儿就是一次存储型 XSS。空名字/空地址同样丢掉，
// 否则用户端会多出一个点不动的空按钮。
func GetTopUpExternalLinks() []TopUpExternalLink {
	links := make([]TopUpExternalLink, 0, len(paymentSetting.ExternalLinks))
	for _, link := range paymentSetting.ExternalLinks {
		name := strings.TrimSpace(link.Name)
		address := strings.TrimSpace(link.URL)
		if link.Amount <= 0 || name == "" || !IsSafeExternalLinkURL(address) {
			continue
		}
		links = append(links, TopUpExternalLink{
			Amount: link.Amount,
			Name:   name,
			URL:    address,
			Icon:   strings.TrimSpace(link.Icon),
		})
	}
	return links
}

// ValidateTopUpExternalLinksJSON 在落库之前把管理员填错的地方直接说出来。
//
// 读取侧的 GetTopUpExternalLinks 会静默丢掉坏条目，光有它的话管理员保存成功、
// 用户端却什么都不多出来，无从查起。
func ValidateTopUpExternalLinksJSON(jsonString string) error {
	if strings.TrimSpace(jsonString) == "" {
		return nil
	}
	var links []TopUpExternalLink
	if err := common.UnmarshalJsonStr(jsonString, &links); err != nil {
		return fmt.Errorf("站外跳转链接必须是 JSON 数组: %w", err)
	}
	for i, link := range links {
		if link.Amount <= 0 {
			return fmt.Errorf("第 %d 条站外跳转链接必须绑定一个大于 0 的充值金额", i+1)
		}
		if strings.TrimSpace(link.Name) == "" {
			return fmt.Errorf("第 %d 条站外跳转链接缺少名称", i+1)
		}
		if !IsSafeExternalLinkURL(link.URL) {
			return fmt.Errorf("第 %d 条站外跳转链接的地址必须以 http:// 或 https:// 开头", i+1)
		}
	}
	return nil
}

// IsSafeExternalLinkURL 判断一个地址能不能安全地交给浏览器打开。
func IsSafeExternalLinkURL(address string) bool {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return parsed.Host != ""
}

func IsPaymentComplianceConfirmed() bool {
	return paymentSetting.ComplianceConfirmed &&
		paymentSetting.ComplianceTermsVersion == CurrentComplianceTermsVersion
}
