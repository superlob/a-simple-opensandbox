// 调用 Manager HTTP API 的薄封装。错误统一为 {error:{code,message}}。

export interface Sandbox {
  id: string;
  status: string;
  image: string;
  created_at: string;
  resources: { cpus?: number; memory_mb?: number; pids_limit?: number };
  endpoint?: string;
}

export interface ApiError {
  code: string;
  message: string;
}

export class ApiException extends Error {
  constructor(public readonly info: ApiError, public readonly status: number) {
    super(info.message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) return undefined as T;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const info: ApiError = data?.error ?? { code: "internal", message: `HTTP ${res.status}` };
    throw new ApiException(info, res.status);
  }
  return data as T;
}

export const api = {
  list: (status?: string) =>
    request<{ items: Sandbox[] }>("GET", `/sandboxes${status ? `?status=${encodeURIComponent(status)}` : ""}`),
  create: () => request<Sandbox>("POST", "/sandboxes", {}),
  start: (id: string) => request<Sandbox>("POST", `/sandboxes/${id}/start`),
  stop: (id: string) => request<Sandbox>("POST", `/sandboxes/${id}/stop`),
  remove: (id: string) => request<void>("DELETE", `/sandboxes/${id}`),
  exec: (id: string, argv: string[]) =>
    request<{ exit_code: number; stdout: string; stderr: string }>("POST", `/sandboxes/${id}/command`, { argv }),
};
