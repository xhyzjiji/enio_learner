export function EmptyState() {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
      <h1 className="text-2xl font-medium text-zinc-200">今天想做点什么？</h1>
      <p className="max-w-md text-sm text-zinc-500">
        我可以调用本地命令和 MCP 工具、检索你的本地文档、记住你的偏好，也可以帮你安排定时任务。
      </p>
    </div>
  )
}
