import { useEffect, useState } from "react";
import { api } from "@/api/client";
import type { SecretInfo } from "@/api/types";

/**
 * 凭证分区：给第三方工具（Tavily、各类 OpenAPI）配密钥。
 *
 * 这里刻意不提供"查看"。接口本身就没有读回值的口子，页面也就无从显示——想核对
 * 只能重新填一遍。这个不方便是故意留的：一个没有鉴权层的本地服务，多开一条读取
 * 凭证的路就是多一个口子。
 *
 * 列表里显示文件路径而不是值，是因为那才是用得上的东西：技能和命令要靠它写出
 * $(cat <路径>)。路径不是秘密。
 *
 * Credential section: keys for third-party tools (Tavily, assorted OpenAPIs).
 *
 * There is deliberately no "reveal". The API exposes no read-back path, so the UI has nothing to
 * show — verifying means retyping. That inconvenience is intentional: on a local service with no
 * auth layer, every additional read path for credentials is another way in.
 *
 * The listing shows the file path rather than the value because the path is what gets used:
 * skills and commands need it to write $(cat <path>). A path is not a secret.
 */
export function SecretSection() {
  const [secrets, setSecrets] = useState<SecretInfo[]>([]);
  const [dir, setDir] = useState("");
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);

  const apply = (r: { secrets: SecretInfo[]; dir: string }) => {
    setSecrets(r.secrets);
    setDir(r.dir);
  };

  const reload = () => {
    api
      .listSecrets()
      .then(apply)
      .catch((e: Error) => setError(e.message));
  };
  useEffect(reload, []);

  // 名称为空时必须在这里拦住，不能交给后端。
  //
  // 名称是路径参数，空名称拼出来是 /api/secrets/，而 PUT /api/secrets/{name} 不匹配
  // 空路径段——请求会落到 SPA 的兜底路由，回一句"接口不存在"，和真实原因毫不相干。
  //
  // 拦的方式是「点了之后说缺什么」，而不是把按钮置灰。置灰看着更规整，实际是个死胡同：
  // 用户看到的只是一个点不动的按钮，没有任何线索指向缺的是哪一项，只能逐个试。
  // 按钮始终可点、点完给出具体原因，走不通的那一步才有出口。
  //
  // 名称的**格式**校验仍然交给后端：规则写在 Go 那一侧，在这里再抄一份正则，
  // 两边迟早会对不上，而对不上的那天没人会发现。这里只管「空」，因为空是路由够不到的
  // 唯一盲区——其余写法后端都会回一条说清规则的 400。
  //
  // An empty name must be stopped here rather than at the backend.
  //
  // The name is a path parameter, so an empty one yields /api/secrets/, which
  // PUT /api/secrets/{name} does not match — the request falls through to the SPA catch-all and
  // returns "no such endpoint", bearing no relation to the actual cause.
  //
  // It is stopped by explaining what is missing on click, not by greying the button out. Greying
  // looks tidier but is a dead end: the user sees an unclickable button with no clue which field
  // is at fault, and can only guess. A button that always responds, and names the reason, leaves
  // a way forward.
  //
  // Name FORMAT validation still belongs to the backend: the rule lives in Go, and a second copy
  // of the regular expression here would eventually drift out of sync, unnoticed. Only emptiness
  // is handled here, because it is the one blind spot routing cannot reach — every other spelling
  // comes back as a 400 that states the rule.
  const submit = async () => {
    if (name.trim() === "") {
      setError("名称不能为空，例如 TAVILY_API_KEY");
      return;
    }
    if (value === "") {
      setError("值不能为空。若用了密码管理器自动填充，请手动在输入框里重新敲一遍");
      return;
    }
    try {
      apply(await api.setSecret(name.trim(), value));
      // 提交后立刻清空输入框，不保留在内存里等用户下次打开分区时又看到。
      // Clear both fields immediately rather than leaving the value in memory for the user to
      // find again next time the section is opened.
      setName("");
      setValue("");
      setAdding(false);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const remove = async (n: string) => {
    try {
      await api.deleteSecret(n);
      setError(null);
      reload();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  return (
    <div className="space-y-2">
      {error && <p className="break-all text-red-400">{error}</p>}
      {secrets.length === 0 && <p className="text-zinc-600">还没有配置凭证</p>}

      {secrets.map((s) => (
        <div key={s.name} className="rounded-md bg-surface px-2 py-2">
          <div className="flex items-center gap-2">
            <span className="flex-1 truncate font-mono text-[11px] text-emerald-400">
              {s.name}
            </span>
            <span className="text-[11px] text-zinc-600">已配置</span>
            <button
              type="button"
              onClick={() => void remove(s.name)}
              className="hover:text-red-400"
            >
              删除
            </button>
          </div>
          <p className="mt-0.5 break-all font-mono text-[11px] text-zinc-600">
            {s.path}
          </p>
        </div>
      ))}

      {adding ? (
        <div className="space-y-1 rounded-md bg-surface p-2">
          <label className="block">
            <span className="text-[11px] text-zinc-600">
              名称（大写字母、数字、下划线）
            </span>
            <input
              value={name}
              placeholder="TAVILY_API_KEY"
              onChange={(e) => setName(e.target.value.toUpperCase())}
              className="mt-0.5 w-full rounded border border-edge bg-panel px-2 py-1 font-mono text-xs text-zinc-200 outline-none focus:border-zinc-500"
            />
          </label>
          <label className="block">
            <span className="text-[11px] text-zinc-600">值</span>
            {/*
              autoComplete 用 new-password 而不是 off：Chrome 对密码类输入框基本无视
              off，照样弹出"保存的密码"浮层去填。被自动填充的字段未必触发 React 的
              onChange，于是界面上看着有值、state 里却是空串——这种不一致排查起来毫无头绪。

              autoComplete is new-password rather than off: Chrome largely ignores off on
              password inputs and still offers to autofill them. An autofilled field does not
              reliably fire React's onChange, leaving the UI showing a value while the state holds
              an empty string — an inconsistency with nothing to go on.
            */}
            <input
              type="password"
              value={value}
              autoComplete="new-password"
              onChange={(e) => setValue(e.target.value)}
              className="mt-0.5 w-full rounded border border-edge bg-panel px-2 py-1 font-mono text-xs text-zinc-200 outline-none focus:border-zinc-500"
            />
          </label>
          <p className="text-[11px] text-zinc-600">
            保存后只能覆盖、不能查看。值写入 0600 权限的文件，不入库、不进对话记录。
          </p>
          <div className="flex gap-2 pt-1">
            <button
              type="button"
              onClick={() => void submit()}
              className="text-blue-400"
            >
              保存
            </button>
            <button
              type="button"
              onClick={() => {
                setAdding(false);
                setValue("");
              }}
              className="text-zinc-500"
            >
              取消
            </button>
          </div>
        </div>
      ) : (
        <button
          type="button"
          onClick={() => setAdding(true)}
          className="text-blue-400"
        >
          ＋ 添加凭证
        </button>
      )}

      {dir && (
        <p className="break-all pt-1 text-[11px] text-zinc-700">
          存放目录：{dir}
        </p>
      )}
    </div>
  );
}
