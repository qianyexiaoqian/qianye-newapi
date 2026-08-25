package violation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
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
// 曾经的堵法是「改地址即清空密钥」。它被撤回了:改地址就只是改地址,保存一个
// 字段不该有副作用,而清空是不可恢复的。
//
// 现在的堵法是把密钥**绑在它写入时的那个地址上**(AIChannel.KeyEndpoint):
// 地址与绑定不一致时,这把密钥一律不出站 —— 试跑直接 400、热路径跳过该渠道 ——
// 直到有人在表单里重填一次密钥,而重填的前提正是他**知道**那把密钥是什么。
//
// 攻击链因此断在第二步,而第一步(改地址)不再有任何副作用:
// 保存照样成功、密钥照样在库里、地址照样是新的。

// TestChangingAIChannelBaseUrlOnlyChangesTheBaseUrl 守撤回本身。
//
// 每一条都断言两件事:密钥这四列一个字节都没动(撤回),以及绑定处在正确的
// 状态(闸门)。少了后者,这张表会在闸门被误删之后依然全绿。
func TestChangingAIChannelBaseUrlOnlyChangesTheBaseUrl(t *testing.T) {
	const (
		origin      = "http://127.0.0.1:11434/v1"
		attacker    = "http://127.0.0.1:18099"
		originalKey = "sk-ORIGINAL-KEY-9911"
	)

	cases := []struct {
		name            string
		newBaseUrl      string
		sendKey         *string
		wantKeyHint     string
		wantKeyEndpoint string
		wantBoundAway   bool
		why             string
	}{
		{
			name: "改地址且不带 api_key:密钥原封不动,但绑定失配",
			// 这一条就是那条越权路径的第 ① 步。它必须成功 —— 改地址就只是改地址。
			newBaseUrl: attacker, sendKey: nil,
			wantKeyHint: maskAIKey(originalKey), wantKeyEndpoint: origin,
			wantBoundAway: true,
			why: "保存不再有副作用;而密钥仍绑在原地址上,所以它不会被送到新地址 —— " +
				"这两件事必须同时成立,少一件要么是没撤回、要么是没堵住",
		},
		{
			name:       "改地址并同时给了新密钥:绑定跟着换到新地址",
			newBaseUrl: attacker, sendKey: strPtr("sk-NEW-KEY-4242"),
			wantKeyHint: maskAIKey("sk-NEW-KEY-4242"), wantKeyEndpoint: attacker,
			wantBoundAway: false,
			why: "运营明确表达了「这把密钥就是给新地址的」,而他能重填就说明他知道它 —— " +
				"这正是闸门要求的东西,一步到位,不额外要求任何别的字段",
		},
		{
			name:       "地址没变(只改了模型名):密钥与绑定都不动",
			newBaseUrl: origin, sendKey: nil,
			wantKeyHint: maskAIKey(originalKey), wantKeyEndpoint: origin,
			wantBoundAway: false,
			why:           "否则改一下模型名就会让渠道停摆,而界面上它看起来配得好好的",
		},
		{
			name:       "地址只差一个结尾斜杠:算同一个端点",
			newBaseUrl: origin + "/", sendKey: nil,
			wantKeyHint: maskAIKey(originalKey), wantKeyEndpoint: origin,
			wantBoundAway: false,
			why:           "归一化后相等就不该误判,否则运营每保存一次就要重填一次密钥",
		},
		{
			name:       "显式传空串:密钥与绑定一起清掉",
			newBaseUrl: origin, sendKey: strPtr(""),
			wantKeyHint: "", wantKeyEndpoint: "",
			wantBoundAway: false,
			why:           "没有密钥就没有可绑的东西;留一个陈旧的地址只会在下次写入前误导排障",
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
			require.NoError(t, applyAIChannelKey(gdb, &row, originalKey))

			var before AIChannel
			require.NoError(t, gdb.Where("id = ?", 1).Take(&before).Error)
			require.True(t, before.HasKey(), "前置条件:这条渠道必须先有密钥")
			require.Equal(t, origin, before.KeyEndpoint,
				"前置条件:写密钥时必须把它绑在当时的地址上")

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
			assert.Equal(t, tc.newBaseUrl, after.BaseUrl, "地址必须一步到位地存进去")
			assert.Equal(t, tc.wantKeyHint != "", after.HasKey(), tc.why)
			assert.Equal(t, tc.wantKeyHint, after.KeyHint, tc.why)
			assert.Equal(t, tc.wantKeyEndpoint, after.KeyEndpoint, tc.why)
			assert.Equal(t, tc.wantBoundAway, after.KeyBoundElsewhere(), tc.why)
			if tc.sendKey == nil {
				// 撤回的核心断言:不带 api_key 的那次保存,密文与 nonce 一个字节都没变。
				assert.Equal(t, []byte(before.KeyCipher), []byte(after.KeyCipher),
					"改地址不该动密文 —— 那正是被撤回的那条规则干的事")
				assert.Equal(t, []byte(before.KeyNonce), []byte(after.KeyNonce))
				assert.Equal(t, before.KeyVersion, after.KeyVersion)
			}
		})
	}
}

// recordingUpstream 是一个记下每一次入站请求的假上游。
//
// 它就是"攻击者控制的那个监听端"。断言的对象是它收到了什么,而不是我们
// 自己返回了什么状态码 —— 只有前者能回答"密钥有没有离开这个进程"。
type recordingUpstream struct {
	*httptest.Server
	mu    sync.Mutex
	auths []string
}

func newRecordingUpstream(t *testing.T) *recordingUpstream {
	t.Helper()
	up := &recordingUpstream{}
	up.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.mu.Lock()
		up.auths = append(up.auths, r.Header.Get("Authorization"))
		up.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"violated\":false}"}}]}`))
	}))
	t.Cleanup(up.Close)
	return up
}

func (u *recordingUpstream) received() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.auths...)
}

// TestAIChannelKeyNeverReachesAnAddressItWasNotWrittenFor 真打一次那条外泄链路。
//
// 三段,缺一条这个闸门就是假的:
//
//	① 攻击者把地址改到自己的监听端再点试跑 —— 他的服务器必须一条请求都收不到。
//	② 同一时刻的**热路径**也必须堵住:只堵试跑是不够的,改完地址之后下一条
//	   真实审核流量会把同一把密钥送到同一个地方。
//	③ 知道密钥的人重填一次之后,渠道立刻恢复可用 —— 否则这不是一道闸,
//	   而是"改过地址的渠道从此报废"。
func TestAIChannelKeyNeverReachesAnAddressItWasNotWrittenFor(t *testing.T) {
	const originalKey = "sk-ORIGINAL-KEY-9911"

	gdb := newAIScopeChannelEnv(t)
	useAIReviewKey(t)

	honest := newRecordingUpstream(t)
	attacker := newRecordingUpstream(t)

	now := common.GetTimestamp()
	row := AIChannel{
		Id: 1, Name: "guard", BaseUrl: honest.URL + "/v1", Model: "m",
		Protocol: AIProtocolJSONPrompt, Weight: 1, Enabled: true, TimeoutMs: 3000,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, gdb.Create(&row).Error)
	require.NoError(t, applyAIChannelKey(gdb, &row, originalKey))

	// AI 审核的热路径需要一条能送审的策略与一行开着的设置,否则装配期会在
	// 读渠道之前就返回 nil,而那样第 ② 段会退化成假绿。
	require.NoError(t, gdb.Create(&AISetting{
		Id: 1, Enabled: true, PreTimeoutMs: 1500, AsyncTimeoutMs: 8000,
		MaxInputChars: defaultAIMaxInputChars,
	}).Error)
	require.NoError(t, gdb.Create(&AIScope{
		Name: "全量", GroupScope: "selfserve", Enabled: true,
		PreSampleRateBps: 10000, Priority: 100,
	}).Error)

	// 前置:没动过地址时,这个渠道是**真的能用的** —— 装配期收得下它。
	rt, err := buildAIRuntime(gdb, true, seedAIVocabulary())
	require.NoError(t, err)
	require.NotNil(t, rt)
	require.Len(t, rt.Channels, 1, "前置条件:未改地址时渠道必须是可用的")
	require.Equal(t, originalKey, rt.Channels[0].APIKey,
		"前置条件:装配期确实会把明文密钥装进运行时,这正是外泄链路的载体")

	// ① 第一步:role=10 只改 base_url,不带 api_key。它必须成功。
	body := `{"name":"guard","base_url":"` + attacker.URL +
		`","model":"m","weight":1,"enabled":true,"timeout_ms":3000,` +
		`"price_in_per_m":"0","price_out_per_m":"0"}`
	c, rec := aiScopeCtx(t, http.MethodPut, "/api/qy/admin/violation/ai-review/channels/1", body)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	adminUpdateAIChannel(c)
	require.Equal(t, http.StatusOK, rec.Code, "改地址必须一步到位,body=%s", rec.Body.String())

	// ① 第二步:点试跑。攻击者的监听端必须一条请求都收不到。
	c, rec = aiScopeCtx(t, http.MethodPost, "/api/qy/admin/violation/ai-review/channels/1/test", "")
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	adminTestAIChannel(c)
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"试跑必须被拒,而且理由要能照着做(重填密钥),body=%s", rec.Body.String())
	assert.Empty(t, attacker.received(),
		"这是本条测试唯一的判据:攻击者的服务器**一个字节都不该收到** —— "+
			"收到了就说明那把 role=10 读不到的密钥已经离开了本进程")

	// ② 热路径同样堵住:改完地址之后,真实审核流量也不会把密钥送过去。
	rt, err = buildAIRuntime(gdb, true, seedAIVocabulary())
	require.NoError(t, err)
	if rt != nil {
		assert.Empty(t, rt.Channels,
			"只堵试跑是不够的:改完地址之后,下一条真实审核流量会把同一把密钥"+
				"送到同一个地方")
	}

	// ③ 知道密钥的人重填一次,渠道立刻恢复 —— 而且这一次密钥**确实**发了出去。
	//    这一段是反向证伪:少了它,一个"永远拒绝试跑"的空实现也能让上面全绿。
	body = `{"name":"guard","base_url":"` + attacker.URL +
		`","model":"m","weight":1,"enabled":true,"timeout_ms":3000,` +
		`"price_in_per_m":"0","price_out_per_m":"0","api_key":"sk-REENTERED-7777"}`
	c, rec = aiScopeCtx(t, http.MethodPut, "/api/qy/admin/violation/ai-review/channels/1", body)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	adminUpdateAIChannel(c)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	c, rec = aiScopeCtx(t, http.MethodPost, "/api/qy/admin/violation/ai-review/channels/1/test", "")
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	adminTestAIChannel(c)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	assert.Equal(t, []string{"Bearer sk-REENTERED-7777"}, attacker.received(),
		"重填之后出站的必须是**新填的**那一把,而原密钥从头到尾没有出去过")
	assert.Empty(t, honest.received(),
		"原地址也不该被打扰:这条链上没有任何一步需要联系它")
}

// TestMigrateAIChannelKeyEndpointBackfillsExistingRows 守存量行的回填。
//
// 零值(空串)按「无绑定」处理,也就是这一列存在之前逐字节一致的行为 ——
// 这是唯一安全的零值方向,因为把空串当成"失配"会让升级那一秒全部存量渠道
// 同时失效,而 AI 审核失败的方向是放行(静默的风控关闭)。
//
// 代价是:回填之前,那条越权路径对存量行仍然是通的。所以回填不是收尾,
// 它就是闸门对存量数据生效的那一步,必须自己有一条测试。
func TestMigrateAIChannelKeyEndpointBackfillsExistingRows(t *testing.T) {
	const origin = "http://127.0.0.1:11434/v1"

	gdb := newAIScopeChannelEnv(t)
	useAIReviewKey(t)
	now := common.GetTimestamp()

	withKey := AIChannel{
		Id: 1, Name: "legacy", BaseUrl: origin, Model: "m",
		Weight: 1, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, gdb.Create(&withKey).Error)
	require.NoError(t, applyAIChannelKey(gdb, &withKey, "sk-LEGACY-1234"))
	// 把绑定抹回空串,造出"这一列存在之前写入的历史行"。
	require.NoError(t, gdb.Model(&AIChannel{}).Where("id = ?", 1).
		Update("key_endpoint", "").Error)

	keyless := AIChannel{
		Id: 2, Name: "no-key", BaseUrl: origin, Model: "m",
		Weight: 1, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, gdb.Create(&keyless).Error)

	var legacy AIChannel
	require.NoError(t, gdb.Where("id = ?", 1).Take(&legacy).Error)
	require.False(t, legacy.KeyBoundElsewhere(),
		"回填之前必须照常可用:空绑定 = 这一列存在之前的行为,升级不改变任何渠道的可用性")

	filled, err := migrateAIChannelKeyEndpoint(context.Background(), gdb)
	require.NoError(t, err)
	assert.EqualValues(t, 1, filled, "没有密钥的渠道不需要绑定,不该被计进去")

	require.NoError(t, gdb.Where("id = ?", 1).Take(&legacy).Error)
	assert.Equal(t, origin, legacy.KeyEndpoint,
		"回填值只能是当前地址:回填这一刻,那把密钥正在被发往这里")
	var stillKeyless AIChannel
	require.NoError(t, gdb.Where("id = ?", 2).Take(&stillKeyless).Error)
	assert.Empty(t, stillKeyless.KeyEndpoint, "没有密钥就没有可绑的东西")

	// 回填之后闸门才对这一行生效。
	legacy.BaseUrl = "http://127.0.0.1:18099"
	assert.True(t, legacy.KeyBoundElsewhere(),
		"回填的全部意义就在这一条:存量行从此也堵得住")

	// 幂等:再跑一次一行都不动。
	again, err := migrateAIChannelKeyEndpoint(context.Background(), gdb)
	require.NoError(t, err)
	assert.Zero(t, again, "迁移必须幂等 —— 每个节点启动时都会各跑一次")
}

func strPtr(s string) *string { return &s }
