import { useEffect } from "react";
import { MessageList } from "@/components/Chat/MessageList";
import { Composer } from "@/components/Chat/Composer";
import { SessionList } from "@/components/Sidebar/SessionList";
import { ConfigPanel } from "@/components/Sidebar/ConfigPanel";
import { useChat } from "@/store/chat";

export function App() {
  const loadSessions = useChat((s) => s.loadSessions);
  const error = useChat((s) => s.error);
  const dismissError = useChat((s) => s.dismissError);

  useEffect(() => {
    void loadSessions();
  }, [loadSessions]);

  return (
    <div className="flex h-full">
      <aside className="flex w-64 shrink-0 flex-col border-r border-edge bg-panel">
        <ConfigPanel />
        <SessionList />
      </aside>
      <main className="flex min-w-0 flex-1 flex-col">
        {error && (
          <div className="flex items-start gap-2 border-b border-red-900/50 bg-red-950/40 px-6 py-2 text-sm text-red-300">
            <span className="flex-1">{error}</span>
            <button
              type="button"
              onClick={dismissError}
              className="text-red-400 hover:text-red-200"
            >
              ×
            </button>
          </div>
        )}
        <MessageList />
        <Composer />
      </main>
    </div>
  );
}
