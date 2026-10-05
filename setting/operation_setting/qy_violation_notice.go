package operation_setting

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 「我的违规记录」页顶部的三条横幅公告。
//
// 项目方要求:超级管理员在系统设置里填写,用户端以横幅展示,用来告知哪些分组会做
// 违规审计。三条够用,每条独立;留空的那条不显示。
//
// 命名口径照 qy_home_group.go 文件头:Qy 前缀、扁平 CamelCase、不以 Key/Token/
// Secret/Enabled/Permission 结尾。读取侧一律直接读 OptionMap,不建包级镜像变量。
// 写入走上游 /api/option(RootAuth),所以只有超级管理员能改。
var QyViolationNoticeOptionKeys = [...]string{
	"QyViolationNotice1",
	"QyViolationNotice2",
	"QyViolationNotice3",
}

// qyViolationNoticeMaxRunes 是单条公告的字数上界。
//
// 它经 /api/status 匿名下发、而 /api/status 是每个页面首屏都要拉的接口,
// 一条公告写成一篇长文会把它拖胖。横幅本来也只适合两三句话。
const qyViolationNoticeMaxRunes = 500

// ValidateQyViolationNotice 校验一条公告。空值放行 —— 清空就是撤下这条横幅。
//
// 允许换行(横幅按原样分行显示);其余控制字符与双向排版符一律拒绝:前者没有
// 正当用途,后者能把一句「以下分组不审计」在视觉上倒过来读。
func ValidateQyViolationNotice(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if n := utf8.RuneCountInString(trimmed); n > qyViolationNoticeMaxRunes {
		return fmt.Errorf("违规记录页公告过长(%d 字,最多 %d 字)", n, qyViolationNoticeMaxRunes)
	}
	for _, r := range trimmed {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || isBidiControl(r) {
			return fmt.Errorf("违规记录页公告含不可见控制字符,请重新输入")
		}
	}
	return nil
}

// QyViolationNotices 是 /api/status 下发的那份:按 1、2、3 的顺序,去掉空条与
// 校验不过的条目(经手改库 / 恢复备份进来的值不走 UpdateOption,读取侧再挡一次)。
//
// 入参是调用方已经持锁读到的 OptionMap:controller.GetStatus 在 RLock 里组装
// payload,RWMutex 不可重入,这里不能自己再拿锁。
func QyViolationNotices(options map[string]string) []string {
	out := make([]string, 0, len(QyViolationNoticeOptionKeys))
	for _, key := range QyViolationNoticeOptionKeys {
		value := strings.TrimSpace(options[key])
		if value == "" || ValidateQyViolationNotice(value) != nil {
			continue
		}
		out = append(out, value)
	}
	return out
}
