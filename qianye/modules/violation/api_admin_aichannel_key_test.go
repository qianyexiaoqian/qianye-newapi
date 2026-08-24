package violation

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AI 审核渠道的上游密钥对 role=10 是刻意 **write-only** 的:列表、详情与 PUT
// 回显一律只给 has_key + key_hint(尾 4 位),全站没有任何一条读回明文的路径。
//
// 但曾经有一条绕过它的路,而且两步都在 role=10 权限之内:
//
//	① PUT 渠道,只改 base_url、**不带 api_key 字段**(指针为 nil = 保持原密钥);
//	② POST 该渠道的 /test —— 出站请求带着 `Authorization: Bearer <原密钥>`
//	   打到攻击者指定的地址上。
//
// 实测走通过:第 ① 步返回 200 且 key_hint 不变(密钥被保留),第 ② 步攻击者的
// 监听端逐字收到那把密钥。对照口径:上游侧把"读渠道 key"判为
// RootAuth + SecureVerificationRequired,qy 这一侧对同一类资产是 role=10 且
// 无二次校验 —— 密钥必须不跟着地址走。
//
// 闸门选"清空"而不是"拒绝保存":拒绝会让"换个地址继续用同一把 key"这件合法的
// 事做不了;清空之后运营在同一个表单里重填一次即可,而重填的前提正是他**知道**
// 那把 key 是什么 —— 那就是这条闸门要求的东西。
func TestUpdatingAIChannelBaseUrlDropsTheStoredKey(t *testing.T) {
	const (
		origin   = "http://127.0.0.1:11434/v1"
		attacker = "http://127.0.0.1:18099"
	)

	cases := []struct {
		name        string
		newBaseUrl  string
		sendKey     *string
		wantHasKey  bool
		wantKeyHint string
		why         string
	}{
		{
			name: "改地址且不带 api_key:必须清掉密钥",
			// 这一条就是被实测走通的那条越权路径的第 ① 步。
			newBaseUrl: attacker, sendKey: nil,
			wantHasKey: false, wantKeyHint: "",
			why: "密钥是发给原地址的凭据;跟着地址走就等于把一把 role=10 读不到的密钥" +
				"交到新地址手里,而下一步的连通性测试同样是 role=10 权限",
		},
		{
			name:       "改地址并同时给了新密钥:用新的",
			newBaseUrl: attacker, sendKey: strPtr("sk-NEW-KEY-4242"),
			wantHasKey: true, wantKeyHint: maskAIKey("sk-NEW-KEY-4242"),
			why: "显式给了就用显式的,清空闸门不该覆盖运营的明确意图",
		},
		{
			name:       "地址没变(只改了模型名):密钥必须保留",
			newBaseUrl: origin, sendKey: nil,
			wantHasKey: true, wantKeyHint: maskAIKey("sk-ORIGINAL-KEY-9911"),
			why: "否则改一下模型名就会把密钥抹掉,而抹掉之后不可恢复",
		},
		{
			name:       "地址只差一个结尾斜杠:算同一个端点,密钥保留",
			newBaseUrl: origin + "/", sendKey: nil,
			wantHasKey: true, wantKeyHint: maskAIKey("sk-ORIGINAL-KEY-9911"),
			why: "归一化后相等就不该误清,否则运营每保存一次就要重填一次密钥",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newAIScopeChannelEnv(t)
			// 必须排在 env 之后:newAIScopeChannelEnv 里的 useTestConfig 会覆盖配置快照。
			useAIReviewKey(t)
			now := common.GetTimestamp()
			row := AIChannel{
				Id: 1, Name: "guard", BaseUrl: origin, Model: "sileader/qwen3guard:0.6b",
				Weight: 1, Enabled: true, TimeoutMs: 3000,
				CreatedAt: now, UpdatedAt: now,
			}
			require.NoError(t, gdb.Create(&row).Error)
			require.NoError(t, applyAIChannelKey(gdb, &row, "sk-ORIGINAL-KEY-9911"))

			var before AIChannel
			require.NoError(t, gdb.Where("id = ?", 1).Take(&before).Error)
			require.True(t, before.HasKey(), "前置条件:这条渠道必须先有密钥")

			body := `{"name":"guard","base_url":"` + tc.newBaseUrl +
				`","model":"sileader/qwen3guard:0.6b","weight":1,"enabled":true,` +
				`"timeout_ms":3000,"price_in_per_m":"0","price_out_per_m":"0"`
			if tc.sendKey != nil {
				body += `,"api_key":"` + *tc.sendKey + `"`
			}
			body += `}`

			c, rec := aiScopeCtx(t, http.MethodPut, "/api/qy/admin/violation/ai-review/channels/1", body)
			c.Params = gin.Params{{Key: "id", Value: "1"}}
			adminUpdateAIChannel(c)
			require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

			var after AIChannel
			require.NoError(t, gdb.Where("id = ?", 1).Take(&after).Error)
			assert.Equal(t, tc.wantHasKey, after.HasKey(), tc.why)
			assert.Equal(t, tc.wantKeyHint, after.KeyHint, tc.why)
			if !tc.wantHasKey {
				assert.Empty(t, after.KeyCipher, "密文必须真的被清掉,不能只改 hint")
				assert.Empty(t, after.KeyNonce)
				assert.Zero(t, after.KeyVersion)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
