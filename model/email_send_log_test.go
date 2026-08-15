package model

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useEmailSendLogDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&EmailSendLog{}))
	DB = db
	t.Cleanup(func() { DB = previousDB })
	return db
}

// TestRecordEmailSendErrorMsgStaysStorable 锁住发件台账落库前的两条约束:
// error_msg 必须是合法 UTF-8,且不超过列宽。
//
// 这不是「测试截断实现」:MySQL 遇到非法字节序列会拒收整条 INSERT
// (Error 1366),PostgreSQL 同理,于是失败记录会整条丢掉 —— 而排查
// 「这个人为什么没收到验证码」靠的就是这些失败记录。SQLite 会照单全收,
// 所以这里断言的是**交给数据库的那个字符串**,而不是某一种数据库的反应。
func TestRecordEmailSendErrorMsgStaysStorable(t *testing.T) {
	cases := []struct {
		name     string
		errorMsg string
	}{
		{
			// 1200 字节,第 emailSendLogErrorMaxLen 个字节落在一个汉字中间。
			name:     "多字节字符恰好被截断点劈开",
			errorMsg: strings.Repeat("错", 400),
		},
		{
			// 上游用 GBK 回中文:错误信息本身就不是合法 UTF-8,且没到截断长度。
			name:     "上游返回的就不是合法UTF-8",
			errorMsg: "550 \xb4\xed\xce\xf3 mailbox unavailable",
		},
		{
			name:     "超长纯ASCII",
			errorMsg: strings.Repeat("x", emailSendLogErrorMaxLen+200),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := useEmailSendLogDB(t)
			RecordEmailSend(common.SMTPSendRecord{
				AccountID: "acct-1",
				Receiver:  "someone@example.com",
				Subject:   "验证码",
				Success:   false,
				ErrorMsg:  tc.errorMsg,
			})

			var stored []EmailSendLog
			require.NoError(t, db.Find(&stored).Error)
			require.Len(t, stored, 1, "失败的发送必须落台账")
			assert.True(t, utf8.ValidString(stored[0].ErrorMsg),
				"交给数据库的 error_msg 不是合法 UTF-8,MySQL/PostgreSQL 会拒收整条记录")
			assert.LessOrEqual(t, len(stored[0].ErrorMsg), emailSendLogErrorMaxLen)
		})
	}

	t.Run("短错误原样落库", func(t *testing.T) {
		db := useEmailSendLogDB(t)
		RecordEmailSend(common.SMTPSendRecord{
			AccountID: "acct-1",
			ErrorMsg:  "550 5.1.1 mailbox unavailable",
		})
		var stored EmailSendLog
		require.NoError(t, db.First(&stored).Error)
		assert.Equal(t, "550 5.1.1 mailbox unavailable", stored.ErrorMsg)
	})
}
