<script lang="ts">
  import type { Connection } from "$lib/api";
  import { formatBytes, formatDuration } from "$lib/format";
  import { Card, CardContent, CardHeader, CardTitle } from "./ui";

  let { connections = [] }: { connections?: Connection[] } = $props();

  // A one-second tick keeps the elapsed times moving; the list itself is refreshed
  // by the page's poll.
  let now = $state(Date.now());
  $effect(() => {
    const timer = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(timer);
  });

  const kindLabel: Record<string, string> = {
    ts: "TS",
    hls: "HLS",
    wav: "WAV",
  };

  const kindHint: Record<string, string> = {
    ts: "长连接，OBS / VLC / mpv",
    hls: "按请求推断，浏览器 / iPhone",
    wav: "长连接，只有 G.711 相机可用",
  };

  function watching(c: Connection): number {
    const since = new Date(c.since).getTime();
    if (!Number.isFinite(since) || since === 0) return 0;
    return Math.max(0, (now - since) / 1000);
  }
</script>

<Card>
  <CardHeader class="flex-row flex-wrap items-baseline justify-between gap-2">
    <CardTitle>
      观众{#if connections.length > 0}<span class="text-muted-foreground"
          >（{connections.length}）</span
        >{/if}
    </CardTitle>
    <span class="text-[11px] text-muted-foreground">
      HLS 没有长连接，20 秒没有请求就当作离开
    </span>
  </CardHeader>

  <CardContent>
    {#if connections.length === 0}
      <p class="text-xs text-muted-foreground">暂时没有人在看。</p>
    {:else}
      <div class="-mx-1 overflow-x-auto">
        <table class="w-full text-xs">
          <thead class="text-muted-foreground">
            <tr class="border-b text-left">
              <th class="py-2 pr-3 font-medium">类型</th>
              <th class="py-2 pr-3 font-medium">来源</th>
              <th class="py-2 pr-3 font-medium">已接收</th>
              <th class="py-2 pr-3 font-medium">看了多久</th>
              <th class="py-2 font-medium whitespace-nowrap">最近请求</th>
            </tr>
          </thead>
          <tbody class="font-mono tabular-nums">
            {#each connections as c (c.kind + c.remote + c.since)}
              <tr class="border-b border-dashed last:border-0">
                <td class="py-2 pr-3" title={kindHint[c.kind] ?? ""}>
                  {kindLabel[c.kind] ?? c.kind}
                </td>
                <td class="py-2 pr-3">{c.remote}</td>
                <td class="py-2 pr-3">{formatBytes(c.bytes)}</td>
                <td class="py-2 pr-3">{formatDuration(watching(c))}</td>
                <td class="py-2 text-muted-foreground">
                  {#if c.last}
                    {formatDuration(Math.max(0, (now - new Date(c.last).getTime()) / 1000))} 前
                    {#if c.requests}<span class="opacity-60">· {c.requests} 次</span>{/if}
                  {:else}
                    <span class="opacity-40">—</span>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </CardContent>
</Card>
