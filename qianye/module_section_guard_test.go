package qianye

import (
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/module"

	"github.com/stretchr/testify/assert"
)

// 每一个注册进来的模块都必须在 config.ModuleGates() 里有一行登记。
//
// 这条与 TestEveryModuleDirectoryIsRegistered 是同一条防线的两截:那条守
// "模块接进了注册表",这条守"模块接进了配置段自检"。少了这条,新加一个模块
// 只要忘了登记,它就永远不会被段级告警覆盖 —— 而它恰恰是最可能被漏掉配置段的
// 那一个(老模块的段早就写进每一份运维文件了,新模块的没有)。
//
// 注意"登记"不等于"必须有开关":apiaddr / paypass / subscription / usergroup
// 登记的是 Section 为空,含义是「这个模块没有配置开关」。刻意要求它们也写一行,
// 是因为"没有开关"是一条需要被写下来的决定,而不是一个可以靠查不到来推断的空白 ——
// 本仓反复栽在"决定"和"遗漏"长得一样上,这里不再留那个歧义。
//
// 但只有本文件这两条是不够的:它们只问"模块名在不在登记表里",于是把一条真有
// 开关的登记降级成 Section 为空(看到报错之后最省事的一行"修法"),告警会静默
// 消失而两条测试照样全绿。挡住那一手的是反向对账
// config.TestEveryPlainBoolSwitchIsGated —— 它从 Config 那一侧问"每个零值 false
// 的 enabled 开关是不是都被某条登记覆盖了"。三条一起才闭环。
func TestEveryModuleHasConfigGate(t *testing.T) {
	gated := make(map[string]bool, len(config.ModuleGates()))
	for _, g := range config.ModuleGates() {
		gated[g.Module] = true
	}

	var missing []string
	for _, m := range module.All() {
		if !gated[m.Name()] {
			missing = append(missing, m.Name())
		}
	}

	assert.Empty(t, missing,
		"以下模块已注册,但没有出现在 qianye/config/sections.go 的 moduleGates 里:%v。"+
			"它们不会被段级自检覆盖 —— 配置文件里少了对应的段时,不会有任何告警,"+
			"模块静默关闭,而代码、编译、单体测试全都正常", missing)
}

// 反向:登记表里不该有已经不存在的模块。
//
// 一条指向已删/已改名模块的登记不会报错,但它会在健康面板上凭空多出一个
// 永远处于 missing_section 的模块,把排障的人引向一个根本不存在的功能。
func TestNoConfigGateWithoutModule(t *testing.T) {
	registered := make(map[string]bool, len(module.All()))
	for _, m := range module.All() {
		registered[m.Name()] = true
	}

	var orphans []string
	for _, g := range config.ModuleGates() {
		if !registered[g.Module] {
			orphans = append(orphans, g.Module)
		}
	}

	assert.Empty(t, orphans,
		"qianye/config/sections.go 的 moduleGates 里登记了这些模块,但注册表里没有它们,"+
			"可能是模块被删除或改名后登记表没跟上:%v", orphans)
}
