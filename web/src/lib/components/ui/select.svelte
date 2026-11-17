<script lang="ts">
  import type { HTMLSelectAttributes } from "svelte/elements";
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";

  interface Props extends Omit<HTMLSelectAttributes, "value"> {
    class?: string;
    value?: string | number;
    children?: Snippet;
  }

  let {
    class: className = "",
    value = $bindable(),
    children,
    ...rest
  }: Props = $props();
</script>

<div class="relative">
  <select
    {...rest}
    bind:value
    class={cn(
      "h-9 w-full appearance-none rounded-md border border-input bg-transparent px-3 pr-8 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/40 disabled:opacity-50",
      className,
    )}
  >
    {@render children?.()}
  </select>
  <svg
    class="pointer-events-none absolute top-1/2 right-3 size-3.5 -translate-y-1/2 text-muted-foreground"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="m6 9 6 6 6-6" />
  </svg>
</div>
