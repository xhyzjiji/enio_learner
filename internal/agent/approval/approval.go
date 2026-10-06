// Package approval 实现 shell 命令的人工确认。
//
// 在此之前 execute 只有"全开"和"全关"两种状态：全关则模型碰不到 shell，全开则它在
// 之后的每一轮里都握着一个完整的 shell，而你无从知道它某一次究竟跑了什么——等你
// 在对话里看到结果时，命令早已执行完毕。本包提供第三种状态：命令在执行**之前**
// 停下来等你点头。
//
// 实现方式是 eino 的中断/恢复，而不是阻塞一个 goroutine 等人点击。两者的差别不在
// 优雅程度，而在能力边界：阻塞等待时，整条执行链——模型上下文、工具调用走到哪一步、
// 后面还剩什么——全都活在那个 goroutine 的栈上，进程一退就什么都不剩。中断则把这些
// 状态交给框架序列化进 checkpoint，于是"等人决定"这件事可以横跨页面刷新、后端重启，
// 甚至换一台机器打开。既然确认的本质就是等一个人，而人可能明天才回来，这个差别是
// 决定性的。
//
// Package approval implements human confirmation for shell commands.
//
// Until now execute had only two states: off, and the model holds a full shell in every
// subsequent turn with no way for you to know what any given invocation actually ran — by the
// time you read the result in the conversation, the command has long since executed. This
// package adds a third state: the command stops and waits for your approval BEFORE running.
//
// It is built on eino's interrupt/resume rather than by blocking a goroutine until someone
// clicks. The difference is not elegance but capability: while blocking, the entire execution
// chain — model context, how far the tool loop got, what remains — lives on that goroutine's
// stack and vanishes with the process. Interrupting instead hands that state to the framework
// to be serialized into a checkpoint, so "waiting for a decision" can span a page refresh, a
// backend restart, even opening the app on another machine. Since confirmation means waiting on
// a person, and a person may come back tomorrow, that difference is decisive.
package approval

import (
	"github.com/cloudwego/eino/schema"
)

// AskInfo 是中断时携带给调用方的信息，说明在等什么。
//
// 必须是具体类型而非 map：checkpoint 走 gob 编码，接口里装着未注册的类型会导致
// 保存失败。更隐蔽的是这个失败是异步冒出来的——事件流里先出现"保存 checkpoint 失败"，
// 再出现"中断成功"，看起来像两件无关的事，而实际后果是这次中断根本恢复不了。
//
// AskInfo travels with an interrupt to say what is being waited on.
//
// It must be a concrete type rather than a map: checkpoints are gob-encoded, and an interface
// holding an unregistered type makes the save fail. More insidiously that failure surfaces
// asynchronously — the event stream shows "failed to save checkpoint" and then "interrupted",
// looking like two unrelated things, when the actual consequence is that the interrupt can
// never be resumed.
type AskInfo struct {
	// Kind 预留给将来别的确认类型，目前只有 exec。
	// Kind is reserved for other confirmation types later; currently only exec exists.
	Kind    string
	Command string
}

// KindExec 表示这是一次 shell 命令确认。
// KindExec marks a shell command confirmation.
const KindExec = "exec"

// Decision 是人工给出的结论，恢复执行时回传给工具。
// Decision is the human verdict, handed back to the tool when execution resumes.
type Decision struct {
	Approved bool
	Reason   string
}

func init() {
	// 必须用 schema.RegisterName 而不是 compose.RegisterSerializableType：
	// 后者只注册进 eino 自己的序列化表，不注册 gob，而 checkpoint 落盘走的正是 gob。
	// 用错的那个注册完，保存时照样报 "type not registered"。
	//
	// schema.RegisterName is required rather than compose.RegisterSerializableType: the latter
	// registers only into eino's own serialization table and not into gob, which is what
	// checkpoint persistence actually uses. Registering with the wrong one still fails to save.
	schema.RegisterName[AskInfo]("agent_approval_ask")
	schema.RegisterName[Decision]("agent_approval_decision")
}
