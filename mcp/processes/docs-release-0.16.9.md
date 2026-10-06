# Processes 0.16.9

Process list rows now use the host theme's hover background for a subtle neutral
highlight, with a gray fallback when the theme token is unavailable. Whole-row
clicking, the pointer cursor and visible keyboard focus remain available.

The built panel is included. The manifest version and runtime source ref both
point to 0.16.9.

Verification: full Go suite, 123 Bun UI/verifier tests, 22 Playwright browser
tests, TypeScript checks and Processes panel build/import checks passed.
