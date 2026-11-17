// The management API served by `micam ctl` behind the fnOS gateway.
//
// Every URL is relative to the page: the app is an iframe at
// /cgi/ThirdParty/micam/index.cgi/, so an absolute path would escape the gateway.

export type StreamState =
  | "connecting"
  | "streaming"
  | "retrying"
  | "stopped"
  | "error";

export interface AppStatus {
  name: string;
  version: string;
  running: boolean;
  pid: number;
  state: StreamState;
  session: number;
  camera?: string;
  transport?: string;
  codec?: string;
  frames: number;
  fps: number;
  viewers: number;
  resyncs: number;
  since?: string;
  updated?: string;
  error?: string;
}

export interface Config {
  user: string;
  user_id: string;
  region: string;
  did: string;
  quality: number;
  pts_lead: number;
  lan: string;
  reconnect: number;
  has_token: boolean;
  pass_token?: string;
}

export interface StatusResponse {
  ok: boolean;
  /** configured reports whether a start could succeed at all. */
  configured: boolean;
  app: AppStatus;
  config: Config;
}

/** base resolves the API root from wherever the CGI is mounted. */
function base(): string {
  const path = window.location.pathname;
  const marker = path.indexOf("index.cgi");
  if (marker >= 0) {
    return path.slice(0, marker + "index.cgi".length) + "/";
  }
  return path.replace(/[^/]*$/, "");
}

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(base() + path, {
    cache: "no-store",
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });

  const text = await response.text();
  let payload: unknown = null;
  try {
    payload = text ? JSON.parse(text) : null;
  } catch {
    throw new Error(text.slice(0, 300) || `HTTP ${response.status}`);
  }

  if (!response.ok) {
    const message =
      (payload as { error?: string } | null)?.error ?? `HTTP ${response.status}`;
    throw new Error(message);
  }
  return payload as T;
}

export function getStatus(): Promise<StatusResponse> {
  return call<StatusResponse>("api/status");
}

export async function getLog(lines = 200): Promise<string> {
  const response = await fetch(`${base()}api/log?lines=${lines}`, {
    cache: "no-store",
  });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return response.text();
}

export function saveConfig(config: Config): Promise<StatusResponse> {
  return call<StatusResponse>("api/config", {
    method: "POST",
    body: JSON.stringify(config),
  });
}

export function control(
  action: "start" | "stop" | "restart",
): Promise<StatusResponse> {
  return call<StatusResponse>("api/control", {
    method: "POST",
    body: JSON.stringify({ action }),
  });
}
