<script lang="ts">
  import { untrack } from "svelte";
  import type { AuthToken, Config, ShareLink, StatusResponse } from "$lib/api";
  import { newToken, saveConfig, shareLinks } from "$lib/api";
  import { copyText } from "$lib/clipboard";
  import {
    Badge,
    Button,
    Card,
    CardContent,
    CardDescription,
    CardHeader,
    CardTitle,
    Input,
    Label,
    Select,
  } from "./ui";

  let {
    config,
    onSaved,
  }: { config: Config; onSaved: (result: StatusResponse) => void } = $props();

  function splitList(value: string): string[] {
    return value
      .split(/[,\s]+/)
      .map((item) => item.trim())
      .filter(Boolean);
  }

  interface Form {
    tokens: AuthToken[];
    allowCidr: string;
    trustIfaces: string;
    noLocalBypass: boolean;
  }

  function fromConfig(c: Config): Form {
    return {
      tokens: (c.tokens ?? []).map((t) => ({ ...t })),
      allowCidr: (c.allow_cidr ?? []).join(", "),
      trustIfaces: (c.trust_ifaces ?? []).join(", "),
      noLocalBypass: c.no_local_bypass ?? false,
    };
  }

  let form = $state<Form>(untrack(() => fromConfig(config)));
  let dirty = $state(false);
  let saving = $state(false);
  let message = $state("");
  let error = $state("");
  let reveal = $state(false);
  let copied = $state<string | null>(null);

  // Polling must never overwrite what someone is in the middle of editing.
  $effect(() => {
    const fresh = fromConfig(config);
    if (!dirty) form = fresh;
  });

  const enabled = $derived(form.tokens.length > 0);

  function edit() {
    dirty = true;
    message = "";
  }

  function mask(secret: string): string {
    if (reveal) return secret;
    if (secret.length <= 10) return "•".repeat(secret.length);
    return `${secret.slice(0, 4)}…${secret.slice(-4)}`;
  }

  async function generate() {
    error = "";
    try {
      const secret = await newToken();
      form.tokens = [
        ...form.tokens,
        { label: `token-${form.tokens.length + 1}`, secret },
      ];
      // The new token is only useful once it is saved and the service restarted,
      // so say so rather than letting it look active.
      edit();
      message = "已生成，点「保存」后生效";
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  function remove(index: number) {
    form.tokens = form.tokens.filter((_, i) => i !== index);
    edit();
  }

  function disable() {
    if (
      form.tokens.length > 0 &&
      !confirm("删除全部 token？删除后任何能连到 8554 的客户端都能直接看画面。")
    ) {
      return;
    }
    form.tokens = [];
    edit();
  }

  async function save() {
    saving = true;
    error = "";
    message = "";
    try {
      const result = await saveConfig({
        ...config,
        tokens: form.tokens,
        allow_cidr: splitList(form.allowCidr),
        trust_ifaces: splitList(form.trustIfaces),
        no_local_bypass: form.noLocalBypass,
      });
      onSaved(result);
      dirty = false;
      message = result.app.running ? "已保存，服务已重启" : "已保存";
      if (result.app.error) error = result.app.error;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  // --- expiring links ---------------------------------------------------

  let ttlHours = $state("12");
  let sharing = $state(false);
  let shareError = $state("");
  let shared = $state<{ expires: string; urls: ShareLink[] } | null>(null);

  async function mint() {
    sharing = true;
    shareError = "";
    shared = null;
    try {
      const result = await shareLinks(Number(ttlHours));
      shared = { expires: result.expires, urls: result.urls ?? [] };
    } catch (e) {
      shareError = e instanceof Error ? e.message : String(e);
    } finally {
      sharing = false;
    }
  }

  function expiresLabel(value: string): string {
    const at = new Date(value);
    if (!Number.isFinite(at.getTime())) return value;
    return at.toLocaleString();
  }

  async function copy(id: string, text: string) {
    const ok = await copyText(text);
    copied = ok ? id : null;
    setTimeout(() => (copied = null), 1600);
  }
</script>

<Card>
  <CardHeader class="flex-row flex-wrap items-start justify-between gap-2">
    <div class="space-y-1.5">
      <CardTitle>访问控制</CardTitle>
      <CardDescription>
        不加凭据时，任何能连到 8554 的客户端都能看画面和听声音。加了之后，只有带凭据的请求能播。
      </CardDescription>
    </div>
    <Badge tone={enabled ? "success" : "warning"}>
      {enabled ? `已启用 · ${form.tokens.length} 个 token` : "未启用"}
    </Badge>
  </CardHeader>

  <CardContent class="space-y-5">
    <div class="space-y-2">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <Label>token</Label>
        <div class="flex items-center gap-1">
          <Button variant="ghost" size="sm" onclick={() => (reveal = !reveal)}>
            {reveal ? "隐藏" : "显示"}
          </Button>
          <Button variant="outline" size="sm" onclick={generate}>生成新 token</Button>
        </div>
      </div>

      {#if form.tokens.length === 0}
        <p class="rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
          当前没有 token。生成一个之后，播放器需要在地址里带上它（下面「播放地址」会自动拼好）。
        </p>
      {:else}
        <ul class="space-y-2">
          {#each form.tokens as token, index (index)}
            <li class="flex flex-wrap items-center gap-2">
              <div class="w-36 shrink-0">
                <Input
                  aria-label="用途，例如：客厅电视"
                  bind:value={token.label}
                  oninput={edit}
                  placeholder="用途"
                />
              </div>
              <code
                class="min-w-0 flex-1 basis-48 truncate rounded-md border bg-muted/40 px-2 py-2 font-mono text-[11px]"
                title={reveal ? token.secret : "点「显示」查看"}
                >{mask(token.secret)}</code
              >
              <Button
                variant="ghost"
                size="sm"
                onclick={() => copy(`t${index}`, token.secret)}
              >{copied === `t${index}` ? "已复制" : "复制"}</Button
              >
              <Button variant="ghost" size="sm" onclick={() => remove(index)}>删除</Button>
            </li>
          {/each}
        </ul>
      {/if}

      {#if form.tokens.length > 0}
        <Button variant="ghost" size="sm" class="text-destructive" onclick={disable}>
          删除全部（关闭访问控制）
        </Button>
      {/if}
      <p class="text-[11px] text-muted-foreground">
        token 只允许 <code class="font-mono">A-Z a-z 0-9 - _</code>：这样它能原样放进 URL、命令行和配置文件，不用转义。
      </p>
    </div>

    <div class="space-y-3 border-t pt-4">
      <div class="space-y-1.5">
        <Label for="cidr">免鉴权网段</Label>
        <Input
          id="cidr"
          bind:value={form.allowCidr}
          oninput={edit}
          placeholder="192.168.0.0/24, 10.0.0.0/8"
        />
        <p class="text-[11px] text-muted-foreground">
          给不会带凭据的客户端用：老电视、DLNA、录像机。留空表示只靠 token。
        </p>
      </div>

      <div class="space-y-1.5">
        <Label for="ifaces">免鉴权网卡</Label>
        <Input
          id="ifaces"
          bind:value={form.trustIfaces}
          oninput={edit}
          placeholder="tailscale0"
        />
        <p class="text-[11px] text-muted-foreground">
          走这块网卡进来的连接免 token。Tailscale 已经由 WireGuard 与 ACL 认证过，再要一次口令是多余的。
          判断依据是本机接受连接的那块网卡，客户端无法冒充。
        </p>
      </div>

      <label class="flex items-start gap-2 text-xs">
        <input
          type="checkbox"
          class="mt-0.5 size-4"
          bind:checked={form.noLocalBypass}
          onchange={edit}
        />
        <span>
          本机（127.0.0.1）也要求凭据
          <span class="mt-0.5 block text-[11px] text-muted-foreground">
            用 TCP 穿透（frp <code class="font-mono">type = tcp</code>）把 8554 暴露到公网时必须打开：穿透进来的请求来自
            127.0.0.1、不带 <code class="font-mono">X-Forwarded-For</code>，和本机播放器分不开，豁免会等于关掉鉴权。打开后本机播放器也要带 token。
          </span>
        </span>
      </label>

      <p class="text-[11px] text-muted-foreground">
        HTTP 类反代和穿透通常会带 <code class="font-mono">X-Forwarded-For</code>，本机豁免会自动失效，不用勾这一项。
      </p>
    </div>

    <div class="flex flex-wrap items-center gap-2 border-t pt-4">
      <Button size="sm" onclick={save} disabled={saving || !dirty}>
        {saving ? "保存中…" : "保存"}
      </Button>
      {#if message}<span class="text-xs text-emerald-600 dark:text-emerald-400">{message}</span>{/if}
      {#if error}<span class="text-xs text-destructive">{error}</span>{/if}
    </div>

    <div class="space-y-2 border-t pt-4">
      <div class="flex flex-wrap items-baseline gap-x-2">
        <span class="text-xs font-medium">分享链接</span>
        <span class="text-[11px] text-muted-foreground">
          给访客用，到期自动失效；换掉签名密钥可以立刻作废全部旧链接
        </span>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <div class="w-36 shrink-0">
          <Select bind:value={ttlHours} aria-label="链接有效期">
            <option value="1">1 小时</option>
            <option value="12">12 小时</option>
            <option value="72">3 天</option>
            <option value="168">7 天</option>
          </Select>
        </div>
        <Button variant="outline" size="sm" onclick={mint} disabled={sharing || !enabled}>
          {sharing ? "生成中…" : "生成链接"}
        </Button>
        {#if !enabled}
          <span class="text-[11px] text-muted-foreground">先启用访问控制</span>
        {/if}
      </div>

      {#if shareError}
        <p class="text-xs break-words text-destructive">{shareError}</p>
      {/if}

      {#if shared}
        <p class="text-[11px] text-muted-foreground">{expiresLabel(shared.expires)} 失效</p>
        <ul class="space-y-1">
          {#each shared.urls as link, index (index)}
            <li class="flex flex-wrap items-center gap-2">
              <code
                class="min-w-0 flex-1 basis-64 truncate rounded-md border bg-muted/40 px-2 py-1.5 font-mono text-[11px]"
                title={link.url}>{link.url}</code
              >
              <Button variant="ghost" size="sm" onclick={() => copy(`s${index}`, link.url)}>
                {copied === `s${index}` ? "已复制" : "复制"}
              </Button>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  </CardContent>
</Card>
