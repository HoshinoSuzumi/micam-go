<script lang="ts">
  import type { HTMLAttributes } from "svelte/elements";
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";

  interface Props extends HTMLAttributes<HTMLHeadingElement> {
    class?: string;
    children?: Snippet;
  }
  let { class: className = "", children, ...rest }: Props = $props();
</script>

<h2 {...rest} class={cn("text-sm font-semibold tracking-tight", className)}>
  {@render children?.()}
</h2>
