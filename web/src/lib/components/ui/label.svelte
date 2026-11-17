<script lang="ts">
  import type { HTMLLabelAttributes } from "svelte/elements";
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";

  interface Props extends HTMLLabelAttributes {
    class?: string;
    children?: Snippet;
  }
  let { class: className = "", children, ...rest }: Props = $props();
</script>

<label {...rest} class={cn("text-xs font-medium text-muted-foreground", className)}>
  {@render children?.()}
</label>
