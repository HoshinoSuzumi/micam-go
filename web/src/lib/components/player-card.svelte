<script lang="ts">
  import { untrack } from "svelte";
  import type { AuthToken, StreamURL } from "$lib/api";
  import { copyText } from "$lib/clipboard";
  import { Button, Card, CardContent, CardHeader, CardTitle, Select } from "./ui";

  let {
    url,
    endpoints = [],
    hlsEnabled = false,
    audioEnabled = false,
    tokens = [],
  }: {
    url: string;
    endpoints?: StreamURL[];
    hlsEnabled?: boolean;
    audioEnabled?: boolean;
    /** Stream credentials; the first one is used unless another is picked. */
    tokens?: AuthToken[];
  } = $props();

  // The select yields strings; the index is converted so the lookup stays
  // obvious and survives a token being deleted.
  let tokenIndex = $state(0);
  const token = $derived(tokens[Number(tokenIndex)]?.secret ?? "");

  // Which row was copied last, so the feedback lands where the click did.
  let copied = $state<string | null>(null);
  let failed = $state<string | null>(null);
  let timer: ReturnType<typeof setTimeout> | undefined;

  // The address the page was opened with is the right default: it is the one that
  // is demonstrably reachable from wherever the user is sitting.
  // A malformed URL from the status file must not take the page down.
  function hostOf(value: string): string {
    try {
      return new URL(value).hostname;
    } catch {
      return value;
    }
  }

  const options = $derived.by(() => {
    const list = [{ value: url, label: "当前页面", iface: "" }];
    const seen = new Set([url]);

    for (const endpoint of endpoints) {
      if (!endpoint.url || seen.has(endpoint.url)) continue;
      seen.add(endpoint.url);
      list.push({
        value: endpoint.url,
        label: endpoint.iface || hostOf(endpoint.url),
        iface: endpoint.iface ?? "",
      });
    }
    return list;
  });

  let selected = $state(untrack(() => url));
  const active = $derived(
    options.some((option) => option.value === selected) ? selected : options[0].value,
  );

  // The chosen interface can disappear (Tailscale down, cable pulled); fall back
  // instead of showing a URL that no longer exists.
  $effect(() => {
    if (!options.some((option) => option.value === selected)) {
      selected = options[0].value;
    }
  });

  // mpv, VLC and ffmpeg all understand user:password@ and turn it into Basic
  // auth, which is the form that needs no quoting and no flags.
  function withUserinfo(value: string, secret: string): string {
    if (!secret) return value;
    // Insert after the scheme so an https stream keeps working.
    return value.replace(/^(https?:\/\/)/, `$1micam:${secret}@`);
  }

  const playable = $derived(withUserinfo(active, token));

  const commands = $derived([
    { id: "mpv", text: `mpv --hwdec=auto ${playable}` },
    { id: "vlc", text: `vlc ${playable}` },
    { id: "ffplay", text: `ffplay -fflags nobuffer ${playable}` },
  ]);

  // The playlist lives next to the stream: same host, same port, different
  // path. Deriving it from the selected address keeps one selector in charge of
  // both, so the two can never point at different interfaces.
  // A <video> element cannot set a header, so the playlist carries the
  // credential in its query string and hands it to every segment it names.
  const hls = $derived.by(() => {
    const base = active.replace(/\.ts$/, ".m3u8");
    return token ? `${base}?token=${encodeURIComponent(token)}` : base;
  });

  // Same for the sound: a separate endpoint, because neither MPEG-TS nor the
  // HLS container can carry the camera's G.711.
  const audio = $derived(withUserinfo(active.replace(/\.ts$/, ".wav"), token));

  async function copy(id: string, text: string) {
    const ok = await copyText(text);
    copied = ok ? id : null;
    failed = ok ? null : id;

    clearTimeout(timer);
    timer = setTimeout(() => {
      copied = null;
      failed = null;
    }, 1600);
  }
</script>

<Card>
  <CardHeader>
    <CardTitle>播放地址</CardTitle>
    <p class="text-xs text-muted-foreground">
      {#if tokens.length > 0}
        地址里已经带上 token；mpv / VLC / ffmpeg 会把它当作 Basic 认证。命令行里含密码，注意别粘到公开的地方。
      {:else}
        没有鉴权，只适合可信网段；外网请走 Tailscale，或在「访问控制」里加一个 token。
      {/if}
    </p>
  </CardHeader>

  <CardContent class="space-y-4">
    {#if tokens.length > 1}
      <div class="flex flex-wrap items-center gap-2">
        <span class="text-xs text-muted-foreground">用哪个 token</span>
        <div class="w-44">
          <Select bind:value={tokenIndex} aria-label="地址里使用哪个 token">
            {#each tokens as item, index (index)}
              <option value={index}>{item.label || `token-${index + 1}`}</option>
            {/each}
          </Select>
        </div>
      </div>
    {/if}

    <div class="flex flex-wrap gap-2">
      <code
        class="min-w-0 flex-1 basis-64 truncate rounded-md border bg-muted/40 px-3 py-2 font-mono text-xs"
        title={playable}>{playable}</code
      >

      {#if options.length > 1}
        <div class="w-40 shrink-0">
          <Select bind:value={selected} aria-label="播放地址使用的网卡">
            {#each options as option (option.value)}
              <option value={option.value}>{option.label}</option>
            {/each}
          </Select>
        </div>
      {/if}

      <Button
        variant="outline"
        size="sm"
        class="h-9 shrink-0"
        onclick={() => copy("url", playable)}
      >
        {copied === "url" ? "已复制" : failed === "url" ? "复制失败" : "复制"}
      </Button>
    </div>

    <div class="-mt-1 space-y-0.5">
      {#each commands as command (command.id)}
        <button
          type="button"
          onclick={() => copy(command.id, command.text)}
          title={command.text}
          class="-mx-2 block w-[calc(100%+1rem)] cursor-pointer truncate rounded-md px-2 py-1 text-left font-mono text-[11px] text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground hover:underline hover:decoration-dotted hover:underline-offset-4"
        >
          {command.text}
          {#if copied === command.id}
            <span class="ml-2 text-emerald-600 dark:text-emerald-400">已复制</span>
          {:else if failed === command.id}
            <span class="ml-2 text-destructive">复制失败，请手动选中</span>
          {/if}
        </button>
      {/each}
    </div>

    {#if hlsEnabled}
      <div class="space-y-2 border-t pt-3">
        <div class="flex flex-wrap items-baseline gap-x-2">
          <span class="text-xs font-medium">HLS 播放地址</span>
          <span class="text-[11px] text-muted-foreground">
            Safari、iPhone、iPad 用这个；播放器会自动按分片取流
          </span>
        </div>

        <div class="flex flex-wrap gap-2">
          <code
            class="min-w-0 flex-1 basis-64 truncate rounded-md border bg-muted/40 px-3 py-2 font-mono text-xs"
            title={hls}>{hls}</code
          >
          <Button
            variant="outline"
            size="sm"
            class="h-9 shrink-0"
            onclick={() => copy("hls", hls)}
          >
            {copied === "hls" ? "已复制" : failed === "hls" ? "复制失败" : "复制"}
          </Button>
        </div>
      </div>
    {/if}

    {#if audioEnabled}
      <div class="space-y-2 border-t pt-3">
        <div class="flex flex-wrap items-baseline gap-x-2">
          <span class="text-xs font-medium">声音（WAV）</span>
          <span class="text-[11px] text-muted-foreground">
            仅摄像头给 G.711 时可用；这台是 Opus，声音在上面那条 HLS 里
          </span>
        </div>

        <div class="flex flex-wrap gap-2">
          <code
            class="min-w-0 flex-1 basis-64 truncate rounded-md border bg-muted/40 px-3 py-2 font-mono text-xs"
            title={audio}>{audio}</code
          >
          <Button
            variant="outline"
            size="sm"
            class="h-9 shrink-0"
            onclick={() => copy("audio", audio)}
          >
            {copied === "audio" ? "已复制" : failed === "audio" ? "复制失败" : "复制"}
          </Button>
        </div>
      </div>
    {/if}
  </CardContent>
</Card>
