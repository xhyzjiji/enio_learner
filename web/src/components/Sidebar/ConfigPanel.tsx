import { useState, type ReactNode } from "react";
import { McpSection } from "./McpSection";
import { CliSection } from "./CliSection";
import { SkillSection } from "./SkillSection";
import { RagSection } from "./RagSection";
import { MemorySection } from "./MemorySection";
import { TaskSection } from "./TaskSection";
import { ModelSection } from "./ModelSection";

interface SectionProps {
  title: string;
  children?: ReactNode;
  hint?: string;
}

/**
 * 配置分区的可折叠外壳。左侧栏上半部的六个分区（文档、MCP、本地命令、技能、
 * 定时任务、模型）都用它包起来。
 *
 * Collapsible shell for a configuration section. All six sections in the upper half of the
 * sidebar (documents, MCP, local commands, skills, scheduled tasks, model) are wrapped in it.
 */
export function Section({ title, children, hint }: SectionProps) {
  const [open, setOpen] = useState(false);
  return (
    <div className="border-b border-edge/60">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm text-zinc-300 hover:text-white"
      >
        <span className="text-xs text-zinc-600">{open ? "▾" : "▸"}</span>
        <span className="flex-1">{title}</span>
      </button>
      {open && (
        <div className="px-3 pb-3 text-xs text-zinc-500">
          {children ?? hint ?? "尚未实现 / not implemented yet"}
        </div>
      )}
    </div>
  );
}

export function ConfigPanel() {
  return (
    <div className="border-b border-edge">
      <div className="px-3 py-2 text-xs font-medium uppercase tracking-wide text-zinc-500">
        配置
      </div>
      <Section title="本地文档 (RAG)">
        <RagSection />
      </Section>
      <Section title="MCP 服务">
        <McpSection />
      </Section>
      <Section title="本地命令">
        <CliSection />
      </Section>
      <Section title="技能">
        <SkillSection />
      </Section>
      <Section title="长期记忆">
        <MemorySection />
      </Section>
      <Section title="定时任务">
        <TaskSection />
      </Section>
      <Section title="模型与限额">
        <ModelSection />
      </Section>
    </div>
  );
}
