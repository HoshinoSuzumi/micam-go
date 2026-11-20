// The management API served by `micam ctl` behind the fnOS gateway.
//
// Every URL is relative to the page: the app is an iframe at
// /cgi/ThirdParty/micam/index.cgi/, so an absolute path would escape the gateway.

export type StreamState =
  | "connecting"
  | "streaming"
  | "retrying"
  | "idle"
  | "stopped"
  | "error";

export interface Camera {
  did: string;
  name: string;
  model: string;
  online: boolean;
}

export interface StreamURL {
  url: string;
  iface?: string;
}

export interface AppStatus {
  name: string;
  version: string;
  running: boolean;
  pid: number;
  state: StreamState;
  /** stage refines "connecting": provisioning, dialing, authenticating, starting-video. */
  stage?: string;
  session: number;
  camera?: string;
  transport?: string;
  codec?: string;
  /** bind is where the HTTP stream listens; a 127.x address is local-only. */
  bind?: string;
  /** urls are the addresses another machine can use, one per interface. */
  urls?: StreamURL[];
  /** hls are the same addresses pointing at the playlist Safari and iOS need. */
  hls?: StreamURL[];
  /** audio is the WAV endpoint, when the camera's sound is enabled. */
  audio?: StreamURL[];
  /** connections lists who is watching, through which output. */
  connections?: Connection[];
  frames: number;
  fps: number;
  viewers: number;
  resyncs: number;
  since?: string;
  updated?: string;
  error?: string;
}

/** A stream credential, named so one device can be revoked on its own. */
export interface AuthToken {
  label: string;
  secret: string;
}

/** One expiring link, minted for a guest. */
export interface ShareLink {
  url: string;
  iface?: string;
}

/** One player: a held-open connection, or an HLS session inferred from requests. */
export interface Connection {
  /** ts, hls or wav */
  kind: string;
  remote: string;
  /** How they got in: loopback, cidr:192.168.0.0/24, token:客厅. */
  auth?: string;
  since: string;
  bytes: number;
  /** Only meaningful for HLS, where the session is inferred. */
  requests?: number;
  last?: string;
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
  /** idle_stop releases the camera after this many minutes with no viewers. */
  idle_stop: number;
  /** hls switches on the playlist Safari and iOS need. */
  hls: boolean;
  /** hls_window is how many segments the playlist keeps. */
  hls_window: number;
  /** audio asks the camera for sound on its own WAV endpoint. */
  audio: boolean;
  /** audio_rate goes into the WAV header. */
  audio_rate: number;
  /** tokens are the stream's own credentials; empty means no access control. */
  tokens: AuthToken[];
  /** allow_cidr lists networks that need no credential. */
  allow_cidr: string[];
  /** trust_ifaces lists interfaces whose clients need no credential. */
  trust_ifaces: string[];
  /** no_local_bypass requires a credential even from 127.0.0.1 (TCP tunnels). */
  no_local_bypass: boolean;
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

/** getCameras lists the account's cameras, so the DID can be picked instead of typed. */
export async function getCameras(): Promise<Camera[]> {
  const body = await call<{ ok: boolean; cameras: Camera[] }>("api/cameras");
  return body.cameras ?? [];
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

/** newToken mints a credential; the server owns the alphabet and the entropy. */
export async function newToken(): Promise<string> {
  const body = await call<{ ok: boolean; token: string }>("api/token", {
    method: "POST",
    body: "{}",
  });
  return body.token;
}

/** shareLinks mints URLs that stop working after ttlHours. */
export function shareLinks(ttlHours: number): Promise<{
  ok: boolean;
  expires: string;
  urls: ShareLink[];
}> {
  return call("api/share", {
    method: "POST",
    body: JSON.stringify({ ttl_hours: ttlHours }),
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
