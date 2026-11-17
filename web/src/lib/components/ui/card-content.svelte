<script lang="ts">
  import type { HTMLAttributes } from "svelte/elements";
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";

  interface Props extends HTMLAttributes<HTMLDivElement> {
    class?: string;
    children?: Snippet;
  }
  let { class: className = "", children, ...rest }: Props = $props();
</script>

<div {...rest} class={cn("p-5 pt-0", className)}>
  {@render children?.()}
</div>
