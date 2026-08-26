package lottery

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 幂等键的规范化结果必须**往下传**,不能只用来做校验。
//
// 改造前:handler 里 `crid := strings.TrimSpace(...)` 只拿去判长度与 `#`,
// 而 entryInputOf 传下去的是 req.ClientRequestId 的**原值**,batchRequestId
// 又从原值派生 `<crid>#i`。于是同一次提交多一个尾空格:
//   - 第 0 注的键在 ChargeEntry 里被 TrimSpace,幂等命中原单;
//   - 第 1..N 注派生出 `"x #1"`,与 `"x#1"` 是两个不同的键 —— 真的再买一遍。
//
// 也就是"半幂等":重放只对第一注生效。这条用例把钱钉死。
func TestEntryIdemKeyIgnoresSurroundingWhitespace(t *testing.T) {
	ext := newPicksCapEnv(t)
	const startQuota = 5_000_000
	main := newBallMainDB(t, startQuota)
	r := picksCapRouter()

	act := seedBallActivity(t, ext, func(a *Activity) {
		a.MaxPicksPerRequest = 10
		a.MaxTotalEntries = 50_000
	})
	picks := picksOf(3)
	// 独立算出的期望:3 注 × 单注参与费,只许扣这一次。
	wantTotal := int64(3) * act.StakeQuota

	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "audws", picks))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	first := decodeEntryBatch(t, body)
	require.Equal(t, 3, first.Accepted)
	require.Equal(t, wantTotal, first.TotalQuota)

	quotaAfterFirst := userQuotaOf(t, main)
	require.EqualValues(t, int64(startQuota)-wantTotal, quotaAfterFirst)

	// 同一个 crid,只多一个尾空格 —— 必须是重放,不许再扣一分钱。
	code, body = callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "audws ", picks))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	replay := decodeEntryBatch(t, body)
	assert.Equal(t, 3, replay.Accepted)
	assert.Equal(t, quotaAfterFirst, userQuotaOf(t, main),
		"尾空格不许把第 1..N 注变成一次全新购买")
	for i := range replay.Entries {
		assert.Equal(t, first.Entries[i].EntryNo, replay.Entries[i].EntryNo,
			"重放必须拿回原来那几张票")
	}
	assert.EqualValues(t, 3, entryRowsOf(t, ext, act.Id))
}

// 只差大小写的 crid 在三方言上必须给出同一个答案。
//
// 键"相不相等"最终由列的排序规则说了算:MySQL 的库默认排序规则大小写不敏感,
// PostgreSQL / SQLite 按字节比较。折叠之前,先发 "abc" 再发 "ABC" 在 MySQL 上
// 被静默吞掉、在 PostgreSQL 上正常扣第二次 —— 同一份代码两种资金结果。
// 折叠之后两侧都按 MySQL(生产方言)的语义走,也就是"当成重放"。
func TestEntryIdemKeyFoldsCaseOnEveryDialect(t *testing.T) {
	ext := newPicksCapEnv(t)
	const startQuota = 5_000_000
	main := newBallMainDB(t, startQuota)
	r := picksCapRouter()

	act := seedBallActivity(t, ext, func(a *Activity) {
		a.MaxPicksPerRequest = 10
		a.MaxTotalEntries = 50_000
	})
	picks := picksOf(2)
	want := int64(2) * act.StakeQuota

	code, body := callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "case-key-a", picks))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	first := decodeEntryBatch(t, body)
	after := userQuotaOf(t, main)
	require.EqualValues(t, int64(startQuota)-want, after)

	code, body = callJSON(t, r, http.MethodPost,
		"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, "CASE-KEY-A", picks))
	require.Equalf(t, http.StatusOK, code, "%s", body)
	again := decodeEntryBatch(t, body)
	assert.Equal(t, first.Entries[0].EntryNo, again.Entries[0].EntryNo)
	assert.Equal(t, after, userQuotaOf(t, main), "大写变体不许再扣一次")
	assert.EqualValues(t, 2, entryRowsOf(t, ext, act.Id))
}

// 非 ASCII 的 crid 必须被拒,而且一分钱都不扣。
//
// 折叠救不了重音不敏感:MySQL 的 utf8mb4_0900_ai_ci 把 'café' 与 'cafe' 判成
// 同一个键,而 PostgreSQL 不会。唯一的办法是不让它们进来。
func TestEntryIdemKeyRejectsNonASCII(t *testing.T) {
	ext := newPicksCapEnv(t)
	const startQuota = 5_000_000
	main := newBallMainDB(t, startQuota)
	r := picksCapRouter()

	act := seedBallActivity(t, ext, func(a *Activity) {
		a.MaxPicksPerRequest = 10
		a.MaxTotalEntries = 50_000
	})
	for _, crid := range []string{"café-1", "订单一", "a b", "a:b"} {
		code, body := callJSON(t, r, http.MethodPost,
			"/lottery/activities/"+act.ActNo+"/entries", entryBody(t, crid, picksOf(1)))
		assert.Equalf(t, http.StatusBadRequest, code, "crid=%q body=%s", crid, body)
	}
	assert.EqualValues(t, startQuota, userQuotaOf(t, main), "被拒的请求不许扣钱")
	assert.EqualValues(t, 0, entryRowsOf(t, ext, act.Id))
}

func userQuotaOf(t *testing.T, main *gorm.DB) int64 {
	t.Helper()
	var u model.User
	require.NoError(t, main.First(&u, ballE2EUserId).Error)
	return int64(u.Quota)
}

func entryRowsOf(t *testing.T, ext *gorm.DB, actId int64) int64 {
	t.Helper()
	var n int64
	require.NoError(t, ext.Model(&Entry{}).Where("act_id = ?", actId).Count(&n).Error)
	return n
}
