package lottery

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// no.go —— 业务单号与密码学随机量的生成。
//
// 这些值有一个共同性质:写错了不会报错,只会静默产生一个别人看不出来的
// 错误结果 —— 可预测的票号意味着可枚举、可挑选。

// 单号前缀。与 qianye/model.KindCode 的两字母码刻意一致,便于人工对账时
// 一眼看出这是哪一类。
const (
	prefixActivity = "LT"
	prefixEntry    = "LE"
	prefixPayout   = "LP"
)

// randomHex 返回 n 字节的密码学随机十六进制。
//
// 失败时 panic 而不是回落到时间戳:crypto/rand 在本进程能启动的任何平台上
// 都不会失败,而"随机源不可用时悄悄换成可预测的源"正是抽奖类系统最经典的
// 后门形状 —— 票号可预测意味着可枚举、可挑选。
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("qianye/lottery: 密码学随机源不可用: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// newNo 生成 <前缀><YYYYMMDD>-<16 位十六进制> 共 27 字符的业务单号。
//
// 日期段只是给人看的(运营按天翻记录),真正的唯一性与不可预测性来自 64 位
// 随机段。总长必须 ≤ 32(三张表的列宽),而 27 还给 IdemKey 的拼接留了余量:
// act_no(27) + ":" + client_request_id(≤64) = 92 ≤ qy_lot_entry.idem_key 的 96。
// 这个余量不是巧合,改任何一段长度都要重新核对它,见 buildIdemKey。
func newNo(prefix string) string {
	return fmt.Sprintf("%s%s-%s", prefix, time.Now().UTC().Format("20060102"), randomHex(8))
}

func newActNo() string    { return newNo(prefixActivity) }
func newEntryNo() string  { return newNo(prefixEntry) }
func newPayoutNo() string { return newNo(prefixPayout) }

// newSecret 生成 32 字节的种子/盐。
func newSecret() string { return randomHex(32) }
