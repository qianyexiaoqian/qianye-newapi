package paypass

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// # 为什么沿用上游登录密码的那一套(bcrypt / common.Password2Hash)
//
// 上游 common/crypto.go 用的是 `bcrypt.GenerateFromPassword(pwd, bcrypt.DefaultCost)`
// 与 `bcrypt.CompareHashAndPassword`,登录密码就存在这套里(model/user.go
// ValidateAndFill → common.ValidatePasswordAndHash)。本模块沿用同一套,理由有三:
//
//  1. bcrypt 本身满足硬要求:自带盐、自适应成本、比对是常量时间(CompareHashAndPassword
//     内部用 subtle.ConstantTimeCompare 比对重算出的摘要)。绝不明文、绝不 MD5/SHA。
//  2. 换成 argon2id 需要引入新依赖并自行管理内存/并行度参数,而这套参数一旦配错
//     (内存给小了)反而弱于 bcrypt 默认成本 —— 在没有专门调参与压测预算的这一轮里,
//     "跟着一个已经在生产跑的正确实现"比"引入一个可能配错的更强算法"更稳。
//  3. 支付密码与登录密码在同一进程里被验证,共用同一个 cost 意味着两者的暴力破解
//     成本一致 —— 不会出现"登录密码很贵、支付密码很便宜"这种木桶短板。
//
// 刻意**不**直接调 common.Password2Hash / common.ValidatePasswordAndHash:
// 那两个函数是上游文件,本模块对它们的调用会在未来某次上游合并里被静默改掉
// (例如上游把 cost 调低)。这里直接调 bcrypt 并把 cost 写进本文件,
// 同时用 Algo 列记录算法,将来要迁移 argon2id 时可以逐行重哈希。

// hashCost 与上游登录密码保持一致(bcrypt.DefaultCost = 10)。
//
// 提高它是安全的(旧哈希仍能被 CompareHashAndPassword 验证,因为 cost 编码在
// 哈希串里),但要连带评估验密接口的 CPU 占用 —— 每次划转都会付这个成本。
const hashCost = bcrypt.DefaultCost

// algoBcrypt 写进 PayPassword.Algo。
const algoBcrypt = "bcrypt"

// 密码强度边界。
//
// 上界 64 字节是给 bcrypt 留余量的:bcrypt 在 72 字节处**静默截断**,
// 一个 100 字节的密码和它的前 72 字节会被判成同一个密码。按字节卡而不是按字符卡,
// 因为截断发生在字节层面 —— 64 个汉字是 192 字节,按字符卡会直接踩进截断区。
const (
	minPasswordBytes = 6
	maxPasswordBytes = 64
)

// dummyHash 是"这个账号没有支付密码"时用来消耗同等 bcrypt 时间的假摘要。
//
// # 它挡的是什么
//
// 没有它,verify 在 Hash 为空时会立刻返回 —— 于是"未设置"是几百微秒、
// "密码错误"是几十毫秒,响应时间成了一个免费的账号状态探针。
// 有了它,三条路径(未设置 / 密码错误 / 已锁定)都恰好付一次同 cost 的 bcrypt。
//
// 用随机密码生成而不是写死一个常量哈希串:写死的常量意味着"提交那个特定密码时
// 未设置密码的账号会验密通过"。随机源失败时 panic 是刻意的 —— 它只发生在
// crypto/rand 不可用的机器上,那种机器上继续跑一个资金系统才是真正的危险。
var dummyHash = sync.OnceValue(func() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic("qianye/paypass: 生成占位摘要失败,crypto/rand 不可用: " + err.Error())
	}
	h, err := bcrypt.GenerateFromPassword([]byte(hex.EncodeToString(buf)), hashCost)
	if err != nil {
		panic("qianye/paypass: 生成占位摘要失败: " + err.Error())
	}
	return string(h)
})

// hashPassword 生成慢哈希摘要。
func hashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), hashCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// slowCompares 统计本进程做过多少次慢哈希比对。
//
// # 它不是可观测性装饰,是一条不变量的**唯一确定性观测点**
//
// "未设置 / 已锁定 / 密码错误三条路径耗时一致"这条要求,唯一诚实的验证方式是
// 证明三条路径都真的跑过一次 bcrypt。用挂钟去比时间是不可靠的断言(CI 上
// 抖动比信号大),而这个计数器让测试可以直接断言"验一个没设过密码的账号,
// 慢哈希比对次数 +1"。把 compareHash 的调用挪到早返回之后(也就是重新引入
// 时间侧信道),这条断言立刻变红。
//
// 代价是每次比对一条 atomic 自增,相对于几十毫秒的 bcrypt 可以忽略。
var slowCompares atomic.Int64

// compareHash 做一次恒定时间比对。hash 为空时用占位摘要顶上,保证耗时不变。
//
// 返回值只有 true/false:bcrypt 的错误(哈希串损坏、cost 非法)与"密码不对"
// 在对外语义上完全一致,分开处理只会多一条泄漏账号状态的分支。
func compareHash(hash, password string) bool {
	slowCompares.Add(1)
	if hash == "" {
		hash = dummyHash()
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// validateStrength 校验支付密码强度。
//
// 判据只有三条,刻意保持简单且可穷举测试:
//  1. 字节长度落在 [6, 64],且是合法 UTF-8;
//  2. 不得是"从头到尾同一个字符"(111111 / aaaaaa)—— 这条不分字符集;
//  3. **纯数字时**不得是连续递增/递减(123456 / 654321)。
//
// 为什么第三条只管纯数字:支付密码在国内的习惯形态就是 6 位数字,而 6 位数字
// 的空间只有 100 万,弱模式高度集中在这两类里。对含字母的密码再加复杂度规则
// 只会把用户推向"写在便签上",收益为负 —— 而且 abcdef 在 62 进制里的处境
// 与 123456 在 10 进制里完全不同。
//
// 真正兜住暴力破解的是锁定策略(输错 N 次锁 M 分钟)+ bcrypt 成本,
// 强度校验只负责挡掉最廉价的那批。
func validateStrength(password string) bool {
	n := len(password)
	if n < minPasswordBytes || n > maxPasswordBytes {
		return false
	}
	// 非法 UTF-8 会在写库时被 utf8mb4 的 MySQL 以 1366 整行拒绝。
	// 密码不落库(只落哈希),但拒绝它仍然更诚实:那种输入必然来自坏掉的客户端。
	if !utf8.ValidString(password) {
		return false
	}
	if isSameRun(password) {
		return false
	}
	return !isAllDigits(password) || !isConsecutiveRun(password)
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isSameRun 判断是否所有字符都相同(111111)。
func isSameRun(s string) bool {
	for i := 1; i < len(s); i++ {
		if s[i] != s[0] {
			return false
		}
	}
	return true
}

// isConsecutiveRun 判断是否为连续递增或递减的数字串(123456 / 654321)。
func isConsecutiveRun(s string) bool {
	up, down := true, true
	for i := 1; i < len(s); i++ {
		if s[i] != s[i-1]+1 {
			up = false
		}
		if s[i] != s[i-1]-1 {
			down = false
		}
	}
	return up || down
}
