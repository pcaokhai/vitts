import type { NextConfig } from "next";

const config: NextConfig = {
  // The image is run as a plain node process, so Next traces its own dependencies and
  // the runtime layer stays small (console/Dockerfile).
  output: "standalone",
  reactStrictMode: true,
  // The console ships no secret, so there is nothing to leak from the bundle. It does
  // ship a version banner, which is worth having when debugging a tenant's report.
  poweredByHeader: false,
};

export default config;
