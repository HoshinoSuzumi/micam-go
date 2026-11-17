<script lang="ts">
  import type { HTMLButtonAttributes } from "svelte/elements";
  import type { Snippet } from "svelte";
  import { cn } from "$lib/utils";

  type Variant = "default" | "secondary" | "outline" | "ghost" | "destructive";
  type Size = "sm" | "default" | "icon";

  interface Props extends HTMLButtonAttributes {
    variant?: Variant;
    size?: Size;
    class?: string;
    children?: Snippet;
  }

  let {
    variant = "default",
    size = "default",
    class: className = "",
    children,
    ...rest
  }: Props = $props();

  const variants: Record<Variant, string> = {
    default: "bg-primary text-primary-foreground hover:bg-primary/90",
    secondary: "bg-secondary text-secondary-foreground hover:bg-secondary/80",
    outline:
      "border border-input bg-transparent hover:bg-accent hover:text-accent-foreground",
    ghost: "hover:bg-accent hover:text-accent-foreground",
    destructive:
      "bg-destructive text-white hover:bg-destructive/90 dark:text-white",
  };

  const sizes: Record<Size, string> = {
    sm: "h-8 gap-1.5 px-3 text-xs",
    default: "h-9 gap-2 px-4 text-sm",
    icon: "size-9",
  };
</script>

<button
  {...rest}
  class={cn(
    "inline-flex shrink-0 cursor-pointer items-center justify-center rounded-md font-medium whitespace-nowrap transition-colors outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50",
    variants[variant],
    sizes[size],
    className,
  )}
>
  {@render children?.()}
</button>
