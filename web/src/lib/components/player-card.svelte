<script lang="ts">
  import { Button, Card, CardContent, CardHeader, CardTitle } from "./ui";

  let { url }: { url: string } = $props();
  let copied = $state(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(url);
      copied = true;
    } catch {
      copied = false;
    }
    setTimeout(() => (copied = false), 1500);
  }
</script>

<Card>
  <CardHeader>
    <CardTitle>播放地址</CardTitle>
    <p class="text-xs text-muted-foreground">
      没有鉴权，只适合可信网段；外网请走 Tailscale 或带认证的反向代理。
    </p>
  </CardHeader>
  <CardContent class="space-y-3">
    <div class="flex gap-2">
      <code
        class="min-w-0 flex-1 truncate rounded-md border bg-muted/40 px-3 py-2 font-mono text-xs"
        >{url}</code
      >
      <Button variant="outline" size="sm" onclick={copy} class="h-9">
        {copied ? "已复制" : "复制"}
      </Button>
    </div>
    <p class="font-mono text-[11px] text-muted-foreground">
      mpv --hwdec=auto {url}
    </p>
  </CardContent>
</Card>
