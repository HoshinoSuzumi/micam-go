<script lang="ts">
  import { control, getStatus, type StatusResponse } from "$lib/api";
  import { stateLabel, stateTone } from "$lib/format";
  import { Badge, Button } from "$lib/components/ui";
  import ConfigCard from "$lib/components/config-card.svelte";
  import ConnectionsCard from "$lib/components/connections-card.svelte";
  import LogCard from "$lib/components/log-card.svelte";
  import PlayerCard from "$lib/components/player-card.svelte";
  import StatusCard from "$lib/components/status-card.svelte";

  let data = $state<StatusResponse | null>(null);
  let error = $state("");
  let busy = $state<"" | "start" | "stop" | "restart">("");

  // The stream always lives on the NAS itself, at the manifest's service port.
  const url = `http://${location.hostname}:8554/cam.ts`;

  async function refresh() {
    try {
      data = await getStatus();
      error = "";
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function action(kind: "start" | "stop" | "restart") {
    busy = kind;
    error = "";
    try {
      data = await control(kind);
      if (!data.ok && data.app.error) error = data.app.error;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
      await refresh();
    } finally {
      busy = "";
    }
  }

  $effect(() => {
    refresh();
    const timer = setInterval(refresh, 2000);
    return () => clearInterval(timer);
  });
</script>

<div class="mx-auto flex max-w-5xl flex-col gap-4 p-4 md:p-6">
  <header class="flex flex-wrap items-start justify-between gap-3">
    <div>
      <h1 class="text-lg font-semibold tracking-tight">micam 摄像头取流</h1>
      <p class="text-xs text-muted-foreground">
        小米摄像头 → MPEG-TS over HTTP，不转码
        {#if data}<span class="opacity-60">· {data.app.version}</span>{/if}
      </p>
    </div>

    <div class="flex flex-wrap items-center gap-2">
      {#if data}
        <Badge tone={stateTone[data.app.state]}>
          <span class="size-1.5 rounded-full bg-current"></span>
          {stateLabel[data.app.state]}
        </Badge>
      {/if}
      {#if data?.app.running}
        <Button
          size="sm"
          variant="outline"
          onclick={() => action("stop")}
          disabled={busy !== ""}
        >
          {busy === "stop" ? "停止中…" : "停止"}
        </Button>
        <Button size="sm" variant="ghost" onclick={() => action("restart")} disabled={busy !== ""}>
          {busy === "restart" ? "重启中…" : "重启"}
        </Button>
      {:else}
        <Button
          size="sm"
          onclick={() => action("start")}
          disabled={busy !== "" || data?.configured === false}
        >
          {busy === "start" ? "启动中…" : "启动"}
        </Button>
      {/if}
    </div>
  </header>

  {#if error}
    <div
      class="rounded-lg border border-destructive/30 bg-destructive/8 px-3 py-2 text-xs break-words text-destructive"
    >
      {error}
    </div>
  {/if}

  {#if data && !data.configured && !data.app.running}
    <div class="rounded-lg border border-amber-500/30 bg-amber-500/8 px-3 py-2 text-xs text-amber-800 dark:text-amber-200">
      还没配置完：在下面填好数字 userId、passToken 和摄像头 DID，保存后会自动启动。
    </div>
  {/if}

  {#if data}
    <StatusCard app={data.app} />
    <PlayerCard
      {url}
      endpoints={data.app.urls ?? []}
      hlsEnabled={data.config.hls}
      audioEnabled={data.config.audio}
    />
    <ConnectionsCard connections={data.app.connections ?? []} />

<ConfigCard
      config={data.config}
      onSaved={(result) => {
        data = result;
        if (!result.ok && result.app.error) error = result.app.error;
      }}
    />
    <LogCard />
  {:else}
    <div class="rounded-lg border border-dashed p-10 text-center text-sm text-muted-foreground">
      正在读取状态…
    </div>
  {/if}
</div>
