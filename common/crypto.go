package common

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"golang.org/x/crypto/bcrypt"
)

func GenerateHMACWithKey(key []byte, data string) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

func GenerateHMAC(data string) string {
	h := hmac.New(sha256.New, []byte(CryptoSecret))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

func Password2Hash(password string) (string, error) {
	passwordBytes := []byte(password)
	hashedPassword, err := bcrypt.GenerateFromPassword(passwordBytes, bcrypt.DefaultCost)
	return string(hashedPassword), err
}

func ValidatePasswordAndHash(password string, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// IsPasswordHash 判断一段值是否是 bcrypt 能真正拿去做一次密钥派生的口令摘要。
//
// 它的用途只有一个:回答"拿这一行去比对口令,会不会因为摘要本身残缺而立刻返回"。
// bcrypt.CompareHashAndPassword 对空串、被截断的串、版本号不认识的串一律走
// ErrHashTooShort/ErrHashVersionTooNew 立即返回,**完全不做密钥派生** ——
// 于是这一类账号的登录拒绝比正常账号快一个数量级,用户名枚举的时序预言机
// 就此重开。防线因此不能只挡空串,必须挡住"不是合法摘要"的整个集合。
//
// 只解析结构、不比较 cost:cost 与 DefaultCost 不一致的摘要仍然是合法摘要,
// bcrypt 会照常派生密钥,那批用户必须还能登录 —— 把它们赶去哨兵分支等于
// 用一条时序加固换掉一批人的登录能力。
func IsPasswordHash(hash string) bool {
	_, err := bcrypt.Cost([]byte(hash))
	return err == nil
}
