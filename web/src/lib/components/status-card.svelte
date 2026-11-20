<script lang="ts">
  import type { AppStatus } from "$lib/api";
  import { formatCount, formatDuration, formatFPS } from "$lib/format";
  import { Card, CardContent, CardHeader, CardTitle } from "./ui";

  let { app }: { app: AppStatus } = $props();

  // A one-second tick keeps the elapsed time moving without re-fetching anything.
  let now = $state(Date.now());
  $effect(() => {
    const timer = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(timer);
  });

  let since = $derived(app.since ? new Date(app.since).getTime() : 0);
  let elapsed = $derived(since ? Math.max(0, (now - since) / 1000) : 0);

  // "连接中" covers five different waits; say which one and for how long, otherwise
  // a wedged session looks exactly like a fast one.
  const stageLabel: Record<string, string> = {
    provisioning: "向米家云申请参数",
    dialing: "连接摄像头",
    authenticating: "认证",
    "starting-video": "请求视频",
    "no-status-file": "没有状态文件（未启用或版本过旧）",
  };

  let detail = $derived.by(() => {
    if (!app.running) return "";
    if (app.state === "connecting") {
      const stage = app.stage ? (stageLabel[app.stage] ?? app.stage) : "准备中";
      return `${stage} · ${formatDuration(elapsed)}`;
    }
    if (app.state === "retrying") return "等待重连";
    if (app.state === "idle") return "没有人观看，摄像头已释放";
    return "";
  });

  let slow = $derived(app.state === "connecting" && elapsed > 20);

  // A loopback bind looks healthy from the NAS and is unreachable from anywhere
  // else: worth saying out loud.
  let loopback = $derived(
    !!app.bind && (app.bind.startsWith("127.") || app.bind.startsWith("[::1]")),
  );

  let uptime = $derived(
    app.state === "streaming" && since ? Math.max(0, (now - since) / 1000) : 0,
  );

  const rows = $derived([
    { label: "摄像头", value: app.camera || "—" },
    { label: "传输", value: app.transport || "—" },
    { label: "编码", value: app.codec || "—" },
    { label: "帧率", value: `${formatFPS(app.fps)} fps` },
    { label: "已转发", value: `${formatCount(app.frames)} 帧` },
    { label: "观看", value: `${app.viewers} 人` },
    { label: "重同步", value: String(app.resyncs) },
    { label: "已运行", value: uptime > 0 ? formatDuration(uptime) : "—" },
    { label: "监听", value: app.bind || "—" },
  ]);
</script>

<Card>
  <CardHeader>
    <CardTitle>连接状态</CardTitle>
    <p class="text-xs text-muted-foreground">
      {#if app.running}
        pid {app.pid} · 第 {app.session || 1} 次会话
        {#if detail}
          ·
          <span class={slow ? "text-amber-600 dark:text-amber-400" : ""}>{detail}</span>
        {/if}
      {:else}
        服务未运行
      {/if}
    </p>
  </CardHeader>

  <CardContent class="space-y-3">
    <dl class="grid grid-cols-2 gap-x-6 gap-y-2.5 sm:grid-cols-3 lg:grid-cols-4">
      {#each rows as row (row.label)}
        <div class="min-w-0">
          <dt class="text-[11px] tracking-wide text-muted-foreground uppercase">
            {row.label}
          </dt>
          <dd class="truncate font-mono text-sm" title={row.value}>{row.value}</dd>
        </div>
      {/each}
    </dl>

    {#if slow}
      <p
        class="rounded-md border border-amber-500/30 bg-amber-500/8 px-3 py-2 text-xs text-amber-800 dark:text-amber-200"
      >
        连接已经花了 {formatDuration(elapsed)}。超过 15 秒还没出画面，多半是摄像头不在线
        （或不在同一网段），看下面的日志确认卡在哪一步。
      </p>
    {/if}

    {#if loopback}
      <p
        class="rounded-md border border-amber-500/30 bg-amber-500/8 px-3 py-2 text-xs text-amber-800 dark:text-amber-200"
      >
        流只监听在本机（{app.bind}），局域网和 Tailscale 都打不开。
        fnOS 包里由 <code class="font-mono">cmd/main</code> 设成 <code class="font-mono"
          >0.0.0.0</code
        >；手动运行时加 <code class="font-mono">--bind 0.0.0.0</code>。
      </p>
    {/if}

    {#if app.error}
      <p
        class="rounded-md border border-destructive/30 bg-destructive/8 px-3 py-2 font-mono text-xs break-words text-destructive"
      >
        {app.error}
      </p>
    {/if}
  </CardContent>
</Card>
