package contract

// Rlimit 资源名常量（只增不改，约束 A2）。
//
// 仅覆盖框架实际下发的三类限制；新增请追加常量，不要改已有值。
const (
	// RLimitCPU 对应 RLIMIT_CPU：CPU 时间秒数。
	RLimitCPU = "cpu"
	// RLimitAS 对应 RLIMIT_AS：地址空间字节数。
	RLimitAS = "as"
	// RLimitNPROC 对应 RLIMIT_NPROC：可派生进程数上限。
	RLimitNPROC = "nproc"
)

// RLimitEnvVar 是宿主下发进程级限制时使用的环境变量名。
//
// 背景：Go 的 syscall.SysProcAttr 在 linux 与 darwin 上都没有 Rlimit 字段，
// os/exec 不提供"为子进程设置 rlimit"的钩子。
// 因此约定由宿主把限制编码进环境变量，**子进程在 main 第一件事自行套用**，
// 早于任何插件业务代码运行。
//
// 格式：RESOURCE:SOFT:HARD[,RESOURCE:SOFT:HARD...]
// 例：cpu:30:60,as:2147483648:2147483648
const RLimitEnvVar = "MILLIPEDE_RLIMITS"

// RLimit 描述一条进程级资源限制。
//
// Soft/Hard 语义与 POSIX setrlimit(2) 一致：
// Soft 是当前生效上限，Hard 是硬上限；Hard 不得小于 Soft。
type RLimit struct {
	// Resource 资源名，取 RLimitCPU / RLimitAS / RLimitNPROC 之一。
	Resource string
	// Soft 软限制。
	Soft uint64
	// Hard 硬限制。
	Hard uint64
}
