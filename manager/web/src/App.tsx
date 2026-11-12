import { useCallback, useEffect, useState } from "react";
import { api, ApiException, Sandbox } from "./api";

function errorText(e: unknown): string {
  if (e instanceof ApiException) return `${e.info.code}: ${e.info.message}`;
  return e instanceof Error ? e.message : String(e);
}

export function App() {
  const [items, setItems] = useState<Sandbox[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [command, setCommand] = useState("echo hello");
  const [output, setOutput] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const refresh = useCallback(async () => {
    try {
      setItems((await api.list()).items);
      setError(null);
    } catch (e) {
      setError(errorText(e));
    }
  }, []);

  useEffect(() => {
    void refresh();
    const timer = setInterval(() => void refresh(), 5000);
    return () => clearInterval(timer);
  }, [refresh]);

  const run = async (fn: () => Promise<unknown>) => {
    setLoading(true);
    try {
      await fn();
      setError(null);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setLoading(false);
      await refresh();
    }
  };

  const current = items.find((s) => s.id === selected) ?? null;

  return (
    <main style={{ fontFamily: "system-ui, sans-serif", padding: 16, maxWidth: 960 }}>
      <h1>Sandbox Manager</h1>
      {error && <p role="alert" style={{ color: "#b00020" }}>{error}</p>}
      <button disabled={loading} onClick={() => run(() => api.create())}>创建 Sandbox</button>

      <table style={{ width: "100%", marginTop: 12, borderCollapse: "collapse" }}>
        <thead>
          <tr>
            <th align="left">ID</th><th align="left">状态</th><th align="left">CPU</th>
            <th align="left">内存(MB)</th><th align="left">操作</th>
          </tr>
        </thead>
        <tbody>
          {items.length === 0 && (
            <tr><td colSpan={5}>暂无 Sandbox</td></tr>
          )}
          {items.map((s) => (
            <tr key={s.id} onClick={() => setSelected(s.id)} style={{ background: s.id === selected ? "#eef" : undefined, cursor: "pointer" }}>
              <td><code>{s.id.slice(0, 8)}</code></td>
              <td>{s.status}</td>
              <td>{s.resources.cpus ?? "-"}</td>
              <td>{s.resources.memory_mb ?? "-"}</td>
              <td onClick={(e) => e.stopPropagation()}>
                {s.status === "stopped"
                  ? <button disabled={loading} onClick={() => run(() => api.start(s.id))}>启动</button>
                  : <button disabled={loading} onClick={() => run(() => api.stop(s.id))}>停止</button>}{" "}
                <button disabled={loading} onClick={() => { if (confirm("删除后文件不可恢复，确定删除？")) run(() => api.remove(s.id)); }}>删除</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      {current && (
        <section style={{ marginTop: 16 }}>
          <h2>执行命令：<code>{current.id.slice(0, 8)}</code></h2>
          {current.status !== "ready" && <p>Sandbox 未运行，需先启动。</p>}
          <input value={command} onChange={(e) => setCommand(e.target.value)} style={{ width: "70%" }} />
          <button
            disabled={loading || current.status !== "ready"}
            onClick={async () => {
              try {
                const r = await api.exec(current.id, command.split(" ").filter(Boolean));
                setOutput(`exit=${r.exit_code}\n${r.stdout}${r.stderr}`);
                setError(null);
              } catch (e) {
                setError(errorText(e));
              }
            }}
          >
            执行
          </button>
          <pre style={{ background: "#111", color: "#eee", padding: 8, minHeight: 60, whiteSpace: "pre-wrap" }}>{output}</pre>
        </section>
      )}
    </main>
  );
}
