import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";

// The UI is served by the fpk's CGI under /cgi/ThirdParty/micam/index.cgi/,
// so every asset URL must stay relative: an absolute /assets/... would resolve
// against the NAS root and 404.
export default defineConfig({
  base: "./",
  plugins: [svelte(), tailwindcss()],
  resolve: {
    alias: { $lib: fileURLToPath(new URL("./src/lib", import.meta.url)) },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    target: "es2022",
    // One JS + one CSS file keeps the CGI's static handler trivial.
    assetsInlineLimit: 8192,
  },
});
