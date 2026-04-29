# Next.js repository

App-Router skeleton with `output: 'standalone'` so the bundled
Dockerfile in `.apteva/Dockerfile` produces a small, runnable image.

## Files

- `app/layout.tsx` — root HTML shell
- `app/page.tsx` — home page
- `next.config.js` — `output: 'standalone'`
- `.apteva/repo.json` — deploy hints (build_cmd, start_cmd, port)
- `.apteva/Dockerfile` — image build for Apteva Deploy
