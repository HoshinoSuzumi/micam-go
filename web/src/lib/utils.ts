import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/** cn merges Tailwind classes the way shadcn does: later classes win. */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
