<script lang="ts">
  import { getLog } from "$lib/api";
  import { Button, Card, CardContent, CardHeader, CardTitle } from "./ui";

  let log = $state("");
  let error = $state("");
  let paused = $state(false);
  let pre = $state<HTMLPreElement>();

  $effect(() => {
    let cancelled = false;

    const tick = async () => {
      if (paused) return;
      try {
        const text = await getLog(200);
        if (!cancelled) {
          log = text;
          error = "";
        }
      } catch (e) {
        if (!cancelled) error = e instanceof Error ? e.message : String(e);
      }
    };

    tick();
    const timer = setInterval(tick, 4000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  });

  // Follow the tail unless someone scrolled up to read something.
  $effect(() => {
    void log;
    if (pre && !paused) pre.scrollTop = pre.scrollHeight;
  });

  function refresh() {
    getLog(200)
      .then((text) => {
        log = text;
        error = "";
      })
      .catch((e) => (error = e instanceof Error ? e.message : String(e)));
  }
</script>

<Card>
  <CardHeader class="flex-row items-center justify-between gap-3">
    <div class="flex flex-col gap-1">
      <CardTitle>运行日志</CardTitle>
      <p class="text-xs text-muted-foreground">
        var/micam.log 的最后 200 行，{paused ? "已暂停" : "每 4 秒刷新"}
      </p>
    </div>
    <div class="flex gap-2">
      <Button variant="outline" size="sm" onclick={() => (paused = !paused)}>
        {paused ? "继续" : "暂停"}
      </Button>
      <Button variant="ghost" size="sm" onclick={refresh}>刷新</Button>
    </div>
  </CardHeader>

  <CardContent>
    {#if error}
      <p class="mb-2 text-xs text-destructive">{error}</p>
    {/if}
    <pre
      bind:this={pre}
      class="max-h-80 overflow-auto rounded-md border bg-muted/30 p-3 font-mono text-[11px] leading-relaxed whitespace-pre-wrap">{log ||
        "（还没有日志）"}</pre>
  </CardContent>
</Card>
