// Package contract 是 Millipede 框架的契约层。
//
// 设计约束（详见 docs/02-插件开发规范.md、docs/03-约束与门禁.md）：
//   - 独立 module、独立版本，禁止依赖主程序内部包（A1）。
//   - 只增不改：禁止修改/删除已有方法签名与字段语义；破坏性变更必须升 Major（A2）。
//   - 所有扩展方法带 context.Context 并返回 error（A3）。
//   - 跨边界只传可序列化值类型（A5）。
//   - 大接口拆小接口，可选能力用独立接口 + 类型断言（A6）。
package contract
