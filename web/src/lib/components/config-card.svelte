<script lang="ts">
  import { untrack } from "svelte";
  import type { Config, StatusResponse } from "$lib/api";
  import { saveConfig } from "$lib/api";
  import {
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

  interface Form {
    user: string;
    user_id: string;
    region: string;
    did: string;
    quality: number;
    pts_lead: number;
    lan: string;
    reconnect: number;
    pass_token: string;
  }

  function fromConfig(c: Config): Form {
    return {
      user: c.user ?? "",
      user_id: c.user_id ?? "",
      region: c.region || "cn",
      did: c.did ?? "",
      quality: c.quality ?? 3,
      pts_lead: c.pts_lead ?? 100,
      lan: c.lan || "prefer",
      reconnect: c.reconnect ?? 5,
      pass_token: "",
    };
  }

  // The initial value only: from here on the effect below owns the sync.
  let form = $state<Form>(untrack(() => fromConfig(config)));
  let dirty = $state(false);
  let saving = $state(false);
  let message = $state("");
  let error = $state("");

  // Only refresh from the server while the form is untouched: polling must never
  // overwrite what someone is in the middle of typing.
  $effect(() => {
    const fresh = fromConfig(config);
    if (!dirty) form = fresh;
  });

  function edit() {
    dirty = true;
    message = "";
  }

  async function save() {
    saving = true;
    error = "";
    message = "";
    try {
      const result = await saveConfig({ ...form, has_token: config.has_token });
      onSaved(result);
      form = { ...form, pass_token: "" };
      dirty = false;
      message = result.app.running ? "已保存，服务已重启" : "已保存";
      if (result.app.error) error = result.app.error;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  function reload() {
    form = fromConfig(config);
    dirty = false;
    error = "";
    message = "";
  }
</script>

<Card>
  <CardHeader>
    <CardTitle>配置</CardTitle>
    <CardDescription>
      保存后会按新参数重启服务。这里和「应用设置」写的是同一份配置。
    </CardDescription>
  </CardHeader>

  <CardContent class="space-y-4">
    <div class="grid gap-4 sm:grid-cols-2">
      <div class="space-y-1.5">
        <Label for="user">小米账号（uid / 手机 / 邮箱）</Label>
        <Input id="user" bind:value={form.user} oninput={edit} autocomplete="off" />
      </div>

      <div class="space-y-1.5">
        <Label for="user-id">数字 userId</Label>
        <Input
          id="user-id"
          bind:value={form.user_id}
          oninput={edit}
          inputmode="numeric"
          autocomplete="off"
          placeholder="2255231260"
        />
      </div>

      <div class="space-y-1.5 sm:col-span-2">
        <Label for="token">passToken</Label>
        <Input
          id="token"
          type="password"
          bind:value={form.pass_token}
          oninput={edit}
          autocomplete="new-password"
          placeholder={config.has_token ? "已保存 · 留空表示不修改" : "从浏览器 Cookie 复制"}
        />
        <p class="text-[11px] text-muted-foreground">
          等同账号密码，只存在 NAS 本地。登录失败时日志会给出服务端返回的错误码。
        </p>
      </div>

      <div class="space-y-1.5">
        <Label for="did">摄像头 DID</Label>
        <Input
          id="did"
          bind:value={form.did}
          oninput={edit}
          inputmode="numeric"
          autocomplete="off"
          placeholder="1221672311"
        />
      </div>

      <div class="space-y-1.5">
        <Label for="region">账号区域</Label>
        <Select id="region" bind:value={form.region} onchange={edit}>
          <option value="cn">中国大陆 (cn)</option>
          <option value="de">德国 (de)</option>
          <option value="sg">新加坡 (sg)</option>
          <option value="us">美国 (us)</option>
          <option value="ru">俄罗斯 (ru)</option>
          <option value="i2">印度 (i2)</option>
        </Select>
      </div>

      <div class="space-y-1.5">
        <Label for="quality">清晰度</Label>
        <Select id="quality" bind:value={form.quality} onchange={edit}>
          <option value={0}>0 自动</option>
          <option value={1}>1 流畅</option>
          <option value={2}>2 高清</option>
          <option value={3}>3 全分辨率</option>
          <option value={4}>4</option>
          <option value={5}>5</option>
        </Select>
      </div>

      <div class="space-y-1.5">
        <Label for="lan">连接方式</Label>
        <Select id="lan" bind:value={form.lan} onchange={edit}>
          <option value="prefer">优先局域网，失败回落中继</option>
          <option value="only">只用局域网</option>
          <option value="off">只用中继</option>
        </Select>
      </div>

      <div class="space-y-1.5">
        <Label for="pts">画面提前量（毫秒）</Label>
        <Input
          id="pts"
          type="number"
          min="0"
          max="2000"
          bind:value={form.pts_lead}
          oninput={edit}
        />
      </div>

      <div class="space-y-1.5">
        <Label for="reconnect">断流重连次数</Label>
        <Input
          id="reconnect"
          type="number"
          min="0"
          max="1000"
          bind:value={form.reconnect}
          oninput={edit}
        />
      </div>
    </div>

    {#if error}
      <p
        class="rounded-md border border-destructive/30 bg-destructive/8 px-3 py-2 text-xs break-words text-destructive"
      >
        {error}
      </p>
    {/if}
    {#if message}
      <p
        class="rounded-md border border-emerald-500/30 bg-emerald-500/8 px-3 py-2 text-xs text-emerald-700 dark:text-emerald-300"
      >
        {message}
      </p>
    {/if}

    <div class="flex items-center gap-2">
      <Button onclick={save} disabled={saving}>
        {saving ? "保存中…" : "保存并应用"}
      </Button>
      <Button variant="ghost" onclick={reload} disabled={saving || !dirty}>
        放弃修改
      </Button>
    </div>
  </CardContent>
</Card>
