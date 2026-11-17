<script lang="ts">
  import type { HTMLAttributes } from "svelte/elements";
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";

  interface Props extends HTMLAttributes<HTMLParagraphElement> {
    class?: string;
    children?: Snippet;
  }
  let { class: className = "", children, ...rest }: Props = $props();
</script>

<p {...rest} class={cn("text-xs text-muted-foreground", className)}>
  {@render children?.()}
</p>
