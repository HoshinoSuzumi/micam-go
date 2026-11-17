<script lang="ts">
  import type { HTMLInputAttributes } from "svelte/elements";
  import { cn } from "$lib/utils";

  interface Props extends Omit<HTMLInputAttributes, "value"> {
    class?: string;
    // Bindable so <Input bind:value={...} /> works from the parent.
    value?: string | number;
  }

  let { class: className = "", value = $bindable(), ...rest }: Props = $props();
</script>

<input
  {...rest}
  bind:value
  class={cn(
    "flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/40 disabled:cursor-not-allowed disabled:opacity-50",
    className,
  )}
/>
