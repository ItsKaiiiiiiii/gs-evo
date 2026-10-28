package proto

// 运行时类型注册表（D14）：新增类型 = 追加一行 + worker 对应 adapter。
// worker 侧负责按 Binaries 在设备 PATH 中定位二进制并建专属 workspace；
// 控制台的"创建运行时"对话框从 GET /admin/runtime-types 读取本表渲染。
type RuntimeTypeDef struct {
	Type     string   `json:"type"`     // 类型标识（task.runtime_id 指派时的稳定名）
	Display  string   `json:"display"`  // 控制台展示名
	Binaries []string `json:"binaries"` // worker 在设备 PATH 中依次查找的二进制名
	Hidden   bool     `json:"hidden"`   // 内部/测试类型不在控制台展示
}

var runtimeTypes = []RuntimeTypeDef{
	{Type: "claude-code", Display: "Claude Code", Binaries: []string{"claude"}},
	{Type: "qodercn", Display: "Qoder", Binaries: []string{"qoder", "qodercn"}},
	{Type: "gsagent", Display: "内置协议 agent", Binaries: []string{"gsagent"}, Hidden: true}, // 测试/调试
}

// RuntimeTypes 可见类型（控制台创建对话框）。
func RuntimeTypes() []RuntimeTypeDef {
	out := make([]RuntimeTypeDef, 0, len(runtimeTypes))
	for _, t := range runtimeTypes {
		if !t.Hidden {
			out = append(out, t)
		}
	}
	return out
}

// AllRuntimeTypes 全部类型（含隐藏）。
func AllRuntimeTypes() []RuntimeTypeDef { return runtimeTypes }

// IsSupportedRuntimeType 类型是否受支持（含隐藏类型）。
func IsSupportedRuntimeType(t string) bool {
	for _, x := range runtimeTypes {
		if x.Type == t {
			return true
		}
	}
	return false
}

// BinariesFor 类型的候选二进制名。
func BinariesFor(t string) []string {
	for _, x := range runtimeTypes {
		if x.Type == t {
			return x.Binaries
		}
	}
	return nil
}
