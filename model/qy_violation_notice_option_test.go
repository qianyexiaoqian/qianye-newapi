package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 「我的违规记录」页的三条横幅公告:写入前校验、匿名下发前再筛一次。
func TestQyViolationNoticesAreValidatedOnWriteAndFilteredOnTheWayOut(t *testing.T) {
	key := operation_setting.QyViolationNoticeOptionKeys[0]

	for _, tc := range []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"留空即撤下", "", false},
		{"多行公告", "以下分组会进行违规审计:\n浅梦号池促销、免费の渠道", false},
		{"刚好 500 字", strings.Repeat("审", 500), false},
		{"超长", strings.Repeat("审", 501), true},
		{"含双向排版符", "以下分组‮不审计", true},
		{"含 NUL", "审计\x00分组", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOptionValue(key, tc.value)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}

	for _, k := range operation_setting.QyViolationNoticeOptionKeys {
		assert.Error(t, validateOptionValue(k, strings.Repeat("审", 501)), "%s 必须同样受校验", k)
	}

	// 下发:按 1、2、3 的顺序,空条与经手改库进来的坏值都不出现在 /api/status 里。
	got := operation_setting.QyViolationNotices(map[string]string{
		operation_setting.QyViolationNoticeOptionKeys[0]: "  第一条  ",
		operation_setting.QyViolationNoticeOptionKeys[1]: "   ",
		operation_setting.QyViolationNoticeOptionKeys[2]: "第三条‮",
	})
	assert.Equal(t, []string{"第一条"}, got)
	assert.NotNil(t, operation_setting.QyViolationNotices(map[string]string{}),
		"没有公告时下发 [] 而不是 null,前端不必判空")
}

func TestRejectedQyViolationNoticeNeverReachesTheDatabase(t *testing.T) {
	db := useFrontendOptionMigrationDB(t)
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{}
	t.Cleanup(func() { common.OptionMap = previousMap })

	key := operation_setting.QyViolationNoticeOptionKeys[1]
	require.Error(t, UpdateOption(key, strings.Repeat("审", 501)))
	requireOptionMissing(t, db, key)

	const good = "浅梦号池促销分组会进行违规审计"
	require.NoError(t, UpdateOption(key, good))
	assert.Equal(t, good, requireOptionValue(t, db, key))
}
