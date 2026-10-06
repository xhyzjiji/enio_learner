import { useRef, useState } from "react";
import { useChat } from "@/store/chat";

export function Composer() {
  const [text, setText] = useState("");
  const sending = useChat((s) => s.sending);
  const send = useChat((s) => s.send);
  const interrupt = useChat((s) => s.interrupt);
  const ta = useRef<HTMLTextAreaElement>(null);

  const submit = () => {
    const value = text.trim();
    if (!value || sending) return;
    setText("");
    if (ta.current) ta.current.style.height = "auto";
    void send(value);
  };

  return (
    <div className="border-t border-edge bg-panel px-6 py-4">
      <div className="mx-auto flex max-w-3xl items-end gap-3">
        <textarea
          ref={ta}
          value={text}
          rows={1}
          placeholder="说点什么…（Enter 发送，Shift+Enter 换行）"
          onChange={(e) => {
            setText(e.target.value);
            // 随内容增高，但封顶在约八行：再高就把对话内容挤没了。
            // Grows with content but caps at roughly eight lines; taller than that starts
            // squeezing the conversation off screen.
            e.target.style.height = "auto";
            e.target.style.height = `${Math.min(e.target.scrollHeight, 200)}px`;
          }}
          onKeyDown={(e) => {
            // 输入法组字过程中的 Enter 是"确认候选词"，不是"发送"。
            // 不判断 isComposing 的话，中文输入每选一次词就会把半句话发出去。
            // Enter during IME composition confirms a candidate rather than sending. Without the
            // isComposing check, every candidate selection in Chinese input would fire off half
            // a sentence.
            if (
              e.key === "Enter" &&
              !e.shiftKey &&
              !e.nativeEvent.isComposing
            ) {
              e.preventDefault();
              submit();
            }
          }}
          className="flex-1 resize-none rounded-xl border border-edge bg-surface px-4 py-3 text-[15px] text-zinc-100 outline-none placeholder:text-zinc-600 focus:border-zinc-500"
        />
        {sending ? (
          <button
            type="button"
            onClick={() => void interrupt()}
            className="rounded-xl bg-zinc-700 px-4 py-3 text-sm text-zinc-100 hover:bg-zinc-600"
          >
            停止
          </button>
        ) : (
          <button
            type="button"
            onClick={submit}
            disabled={!text.trim()}
            className="rounded-xl bg-blue-600 px-4 py-3 text-sm text-white disabled:bg-zinc-800 disabled:text-zinc-600"
          >
            发送
          </button>
        )}
      </div>
    </div>
  );
}
