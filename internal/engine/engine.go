// Package engine defines the abstract AI engine interface used by feisuo.
// Both "pi" and "codebuddy" implement this interface.
package engine

import "context"

// Engine 是 feisuo 的 AI 引擎抽象（pi / codebuddy）。
// 每个会话持有一个常驻引擎实例：首次收到该会话消息时启动，
// 之后所有消息通过 stdin 发 JSON 命令，流式事件从 stdout 读，
// result 事件触发完成通知。会话之间上下文完全隔离。
type Engine interface {
	// Execute 向指定会话发送 prompt。流式回调 onChunk，完成后 onDone。
	// 会话首次使用时自动启动进程；onDone 可被 err 非 nil 调用（超时/进程异常）。
	Execute(ctx context.Context, session, prompt string, onChunk func(string), onDone func(string, error))
	// ExecuteSteer 立即打断当前正在跑的 agent 任务，优先处理这条消息（用于纠正/补充等）。
	// 与 Execute 不同的语义：打断当前 turn，注入新 prompt 继续对话。
	ExecuteSteer(ctx context.Context, session, prompt string, onChunk func(string), onDone func(string, error))
	// Steer 向指定会话发送 steer 消息（不等待流式返回），用于 busy 时的纠正注入。
	Steer(session, msg string) error
	// Abort 中断指定会话当前正在跑的 agent 任务（不销毁会话、不重启进程）。
	Abort(session string) error
	// Reset 清空会话上下文（等价 /new）。若 agent 正在跑，需立即结束等待者。
	Reset(session string) error
	// Kill 关闭指定会话的常驻进程（真正释放资源）。
	// 会话不存在或未启动时静默返回。
	Kill(session string)
	// Close 关闭所有常驻进程。
	Close()
	// SessionCount 返回活跃常驻进程数（调试用）。
	SessionCount() int
	// SetSessionList 注入会话列表提供者，用于 bridge 协作协议（可转发给哪些会话）。
	SetSessionList(fn func() []string)
	// SetSystemPromptOf 注入会话角色提供者，用于启动时注入自定义系统提示词。
	SetSystemPromptOf(fn func(string) string)
}
