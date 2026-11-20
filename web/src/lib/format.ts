import type { StreamState } from "./api";

export const stateLabel: Record<StreamState, string> = {
  connecting: "连接中",
  streaming: "推流中",
  retrying: "重连中",
  idle: "待机",
  stopped: "已停止",
  error: "出错",
};

export const stateTone: Record<
  StreamState,
  "muted" | "success" | "warning" | "danger" | "info"
> = {
  connecting: "info",
  streaming: "success",
  retrying: "warning",
  idle: "info",
  stopped: "muted",
  error: "danger",
};

/** formatDuration renders seconds as 3h46m / 12m08s / 34s. */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "—";
  const total = Math.floor(seconds);
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const secs = total % 60;
  if (hours > 0) return `${hours}h${String(minutes).padStart(2, "0")}m`;
  if (minutes > 0) return `${minutes}m${String(secs).padStart(2, "0")}s`;
  return `${secs}s`;
}

/** formatCount keeps big frame counts readable without lying about precision. */
export function formatCount(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0";
  if (n >= 10_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 100_000) return `${Math.round(n / 1000)}k`;
  return String(n);
}

export function formatFPS(fps: number): string {
  if (!Number.isFinite(fps) || fps <= 0) return "0";
  return fps >= 10 ? fps.toFixed(0) : fps.toFixed(1);
}

// formatBytes keeps byte counts readable in the viewer list: the difference
// between 3 MB and 300 MB is the difference between "just opened it" and "left it
// running all night".
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";

  const units = ["B", "KB", "MB", "GB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }

  return `${value >= 10 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`;
}