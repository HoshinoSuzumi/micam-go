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

<div {...rest} class={cn("flex flex-col gap-1 p-5 pb-3", className)}>
  {@render children?.()}
</div>
