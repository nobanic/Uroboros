import { fileURLToPath } from "node:url";

// Static export: `NEXT_EXPORT=1 next build` produces a pure static directory at web/out that can be dropped
// straight into the nginx web root. Development (next dev) does not set this variable, keeping the /api proxy and hot reload.
const isExport = process.env.NEXT_EXPORT === "1";
// Vercel demo: the whole site runs on mock, with no backend and no need for the /api proxy.
const isMock = process.env.NEXT_PUBLIC_MOCK === "1";

/** @type {import('next').NextConfig} */
const nextConfig = {
  // Avoid a parent directory's lockfile affecting root inference and asset path generation.
  turbopack: { root: fileURLToPath(new URL(".", import.meta.url)) },
  reactCompiler: true,
  // Allow dev assets (HMR) to be accessed from LAN IPs; add or remove as needed.
  // In dev, open up /_next/* and HMR to any IPv4 origin (unaffected by changing LAN IPs).
  // Note: for security Next forbids a bare "*"; use segmented wildcards; "*.*.*.*" matches any IPv4.
  allowedDevOrigins: ["*.*.*.*"],
  compiler: {
    removeConsole: process.env.NODE_ENV === "production",
  },
  ...(isExport
    ? {
        // Pure static export: no Node runtime; images are not optimized; each route produces <route>/index.html.
        output: "export",
        images: { unoptimized: true },
        trailingSlash: true,
      }
    : isMock
      ? {
          // Vercel mock demo: no backend, no /api proxy needed.
          images: { unoptimized: true },
        }
      : {
          // Development: proxy /api/* to the Go backend (default :8787, overridable via AUTOPENTEST_API).
          async rewrites() {
            const backend = process.env.AUTOPENTEST_API ?? "http://localhost:8787";
            return [{ source: "/api/:path*", destination: `${backend}/api/:path*` }];
          },
        }),
};

export default nextConfig;
