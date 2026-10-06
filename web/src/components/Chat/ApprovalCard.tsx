import { useState } from "react";
import type { ApprovalRequest } from "../../api/types";

/**
 * 命令执行前的确认卡片。
 *
 * 命令原文用等宽字体完整展示且不折行省略——这是你做判断的唯一依据，
 * 截断它等于让你在看不清内容的情况下点同意。
 *
 * 这张卡片没有倒计时。等待不消耗后端任何资源：那一轮的执行状态已经序列化进
 * checkpoint，没有 goroutine 挂着，也没有连接吊着。所以催促你做决定没有任何技术
 * 理由，而催促的代价是真实的——人离开五分钟回来发现命令被自动拒绝了。
 *
 * Confirmation card shown before a command runs.
 *
 * The command is displayed in full in a monospace font and never elided: it is the sole basis
 * for your decision, and truncating it would mean approving something you cannot fully read.
 *
 * There is no countdown. Waiting costs the backend nothing: that turn's execution state is
 * already serialized into a checkpoint, with no goroutine blocked and no connection held open.
 * So there is no technical reason to rush the decision, while the cost of rushing is real — step
 * away for five minutes and come back to find the command auto-refused.
 */
export function ApprovalCard({
  request,
  onResolve,
}: {
  request: ApprovalRequest;
  onResolve: (requestId: string, approved: boolean) => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);

  // 结论以后端记录为准，不靠本地 state 记忆。卡片可能是页面刚加载时从库里捞出来的，
  // 那时组件根本没经历过点击这件事。
  // The verdict comes from the backend record rather than local state: the card may have been
  // restored from the database on page load, when this component never witnessed a click.
  const decided = request.status !== "pending";

  const decide = async (approved: boolean) => {
    setBusy(true);
    setFailed(false);
    try {
      await onResolve(request.id, approved);
    } catch {
      setFailed(true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="self-start w-full max-w-[85%] rounded-xl border border-amber-500/40 bg-amber-500/5 px-4 py-3">
      <div className="mb-2 flex items-center gap-2 text-xs text-amber-400">
        <span>⚠</span>
        <span>Agent 请求执行命令</span>
      </div>

      <pre className="mb-3 overflow-x-auto whitespace-pre-wrap break-all rounded-md bg-zinc-900 px-3 py-2 font-mono text-[12px] leading-relaxed text-zinc-200">
        {request.command}
      </pre>

      {!decided ? (
        <div className="flex items-center gap-2">
          <button
            type="button"
            disabled={busy}
            onClick={() => void decide(true)}
            className="rounded bg-amber-600 px-3 py-1 text-xs text-white disabled:opacity-50"
          >
            允许执行
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={() => void decide(false)}
            className="rounded border border-edge px-3 py-1 text-xs text-zinc-400 hover:text-white disabled:opacity-50"
          >
            拒绝
          </button>
          <span className="text-[10px] text-zinc-600">
            不限时，随时回来决定即可
          </span>
        </div>
      ) : (
        <p className="text-xs text-zinc-500">
          {request.status === "approved" && "已允许，命令已执行。"}
          {request.status === "denied" && "已拒绝，命令未执行。"}
          {request.status === "abandoned" &&
            "已作废：你在等待期间发了新消息，命令未执行。"}
        </p>
      )}
      {failed && (
        <p className="mt-2 text-xs text-red-400">
          提交失败，可能已在别处处理过。刷新页面看看最新状态。
        </p>
      )}
    </div>
  );
}
