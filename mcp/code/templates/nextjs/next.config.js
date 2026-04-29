/** @type {import('next').NextConfig} */
const nextConfig = {
  // Required for the Dockerfile in .apteva/ — produces a self-contained
  // server.js that the runner stage copies and starts.
  output: 'standalone',
  reactStrictMode: true,
};

module.exports = nextConfig;
