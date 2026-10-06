import { useEffect, useRef } from "react";
import clsx from "clsx";
import { useChat } from "@/store/chat";
import { ToolCallCard } from "./ToolCallCard";
import { EmptyState } from "./EmptyState";
import { Markdown } from "./Markdown";
import { ApprovalCard } from "./ApprovalCard";

export function MessageList() {
  const messages = useChat((s) => s.messages);
  const notice = useChat((s) => s.notice);
  const resolveApproval = useChat((s) => s.resolveApproval);
  const bottom = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const scroller = useRef<HTMLDivElement>(null);

  // 只在用户本来就贴着底部时才自动滚动。用户往上翻看历史的时候把他强行拽回
  // 底部，是聊天界面里最烦人的一种行为。
  // Auto-scroll only when the user is already pinned to the bottom. Yanking someone back down
  // while they are scrolled up reading history is the single most irritating behaviour a chat
  // UI can have.
  useEffect(() => {
    if (pinned.current) bottom.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages]);

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  };

  if (messages.length === 0) return <EmptyState />;

  return (
    <div
      ref={scroller}
      onScroll={onScroll}
      className="flex-1 overflow-y-auto px-6 py-6"
    >
      <div className="mx-auto flex max-w-3xl flex-col gap-5">
        {messages.map((m) =>
          m.role === "approval" && m.approval ? (
            <ApprovalCard
              key={m.key}
              request={m.approval}
              onResolve={resolveApproval}
            />
          ) : m.role === "tool" ? (
            <ToolCallCard
              key={m.key}
              name={m.toolName ?? "tool"}
              result={m.content}
            />
          ) : (
            <div
              key={m.key}
              className={clsx(
                "flex",
                m.role === "user" ? "justify-end" : "justify-start",
              )}
            >
              <div
                className={clsx(
                  "max-w-[85%] break-words rounded-2xl px-4 py-3 text-[15px] leading-relaxed",
                  m.role === "user"
                    ? "whitespace-pre-wrap bg-blue-600 text-white"
                    : "bg-surface text-zinc-100 ring-1 ring-edge",
                )}
              >
                {m.reasoning && (
                  <div className="mb-2 whitespace-pre-wrap border-l-2 border-zinc-600 pl-3 text-sm text-zinc-400">
                    {m.reasoning}
                  </div>
                )}
                {/* 用户消息按原样显示：他输入的 * 和 # 是字面意思，不该被当成格式。
                    User messages stay literal: the * and # they typed mean themselves, not formatting. */}
                {m.role === "user"
                  ? m.content
                  : m.content && <Markdown content={m.content} />}
                {m.streaming && !m.content && (
                  <span className="inline-block h-4 w-2 animate-pulse bg-zinc-400 align-middle" />
                )}
                {m.toolCalls?.map((tc) => (
                  <ToolCallCard
                    key={tc.id}
                    name={tc.name}
                    args={tc.arguments}
                  />
                ))}
              </div>
            </div>
          ),
        )}
        {notice && (
          <div className="self-center rounded-full bg-zinc-800 px-4 py-1 text-xs text-zinc-400">
            {notice}
          </div>
        )}
        <div ref={bottom} />
      </div>
    </div>
  );
}
