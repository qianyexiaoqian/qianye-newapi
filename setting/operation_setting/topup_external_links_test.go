package operation_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 外链的地址会原样变成用户端的 href，所以「什么能出去」是个安全契约，不是格式偏好。
func TestGetTopUpExternalLinksDropsUnusableEntries(t *testing.T) {
	original := paymentSetting.ExternalLinks
	t.Cleanup(func() { paymentSetting.ExternalLinks = original })

	paymentSetting.ExternalLinks = []TopUpExternalLink{
		{Amount: 50, Name: "  50 元兑换码  ", URL: "  https://shop.example.com/item/50  ", Icon: " LuExternalLink "},
		{Amount: 100, Name: "内网商店", URL: "http://shop.internal:8080/buy"},
		{Amount: 50, Name: "伪协议", URL: "javascript:alert(1)"},
		{Amount: 50, Name: "空地址", URL: "   "},
		{Amount: 50, Name: "   ", URL: "https://shop.example.com/item/100"},
		{Amount: 50, Name: "相对路径", URL: "/shop/item/50"},
		{Amount: 0, Name: "没绑金额", URL: "https://shop.example.com/item/200"},
	}

	links := GetTopUpExternalLinks()

	require.Len(t, links, 2)
	assert.Equal(t, TopUpExternalLink{
		Amount: 50,
		Name:   "50 元兑换码",
		URL:    "https://shop.example.com/item/50",
		Icon:   "LuExternalLink",
	}, links[0])
	assert.Equal(t, "http://shop.internal:8080/buy", links[1].URL)
}

func TestValidateTopUpExternalLinksJSON(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr string
	}{
		{
			name:  "empty value clears the list",
			value: "  ",
		},
		{
			name:  "valid entries pass",
			value: `[{"amount":50,"name":"50 元兑换码","url":"https://shop.example.com/item/50","icon":"LuExternalLink"}]`,
		},
		{
			name:    "an object is not a list",
			value:   `{"amount":50,"name":"x","url":"https://example.com"}`,
			wantErr: "站外跳转链接必须是 JSON 数组",
		},
		{
			name:    "an unbound entry could never be shown",
			value:   `[{"name":"x","url":"https://example.com"}]`,
			wantErr: "第 1 条站外跳转链接必须绑定一个大于 0 的充值金额",
		},
		{
			name:    "a nameless entry would render a blank button",
			value:   `[{"amount":50,"name":" ","url":"https://example.com"}]`,
			wantErr: "第 1 条站外跳转链接缺少名称",
		},
		{
			name:    "a javascript URL is rejected",
			value:   `[{"amount":50,"name":"ok","url":"https://example.com"},{"amount":100,"name":"bad","url":"javascript:alert(1)"}]`,
			wantErr: "第 2 条站外跳转链接的地址必须以 http:// 或 https:// 开头",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTopUpExternalLinksJSON(tc.value)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
