<script lang="ts">
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";

  type Tone = "muted" | "success" | "warning" | "danger" | "info";

  interface Props {
    tone?: Tone;
    class?: string;
    children?: Snippet;
  }
  let { tone = "muted", class: className = "", children }: Props = $props();

  const tones: Record<Tone, string> = {
    muted: "border-transparent bg-secondary text-secondary-foreground",
    success:
      "border-emerald-500/30 bg-emerald-500/12 text-emerald-700 dark:text-emerald-300",
    warning:
      "border-amber-500/30 bg-amber-500/12 text-amber-700 dark:text-amber-300",
    danger:
      "border-destructive/30 bg-destructive/12 text-destructive dark:text-red-300",
    info: "border-sky-500/30 bg-sky-500/12 text-sky-700 dark:text-sky-300",
  };
</script>

<span
  class={cn(
    "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs font-medium",
    tones[tone],
    className,
  )}
>
  {@render children?.()}
</span>
