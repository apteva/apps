# Neural — native Apteva app

Neural is a Go/app-sdk sidecar with a built-in React **project.page** panel,
following CRM's app packaging and dashboard integration. Install the app in
an Apteva project, then open **Neural** from the project's navigation. Its
dashboard route is `/apps/neural/page`.

There is no separate frontend server, standalone application, or iframe.
Apteva imports `/ui/NeuralPanel.mjs` through its authenticated app proxy and
mounts it in the dashboard's React tree. The panel uses the host's React,
theme tokens, project/install context, session authentication, and AppBus.

## Build and install

From this directory:

```sh
bun run build.ts
go build -o neural .
go test -race ./...
bun test ui/network.test.ts
```

The workspace-wide equivalent panel build is
`bun run scripts/build-panels.ts --app neural` from the apps repository.
The generated panel and source map ship with this app. Changes to this panel
do not require a dashboard or server rebuild.

Install **Neural Studio** from Apteva's Marketplace, or use the
[v0.1.1 manifest](https://raw.githubusercontent.com/apteva/apps/neural/v0.1.1/mcp/neural/apteva.yaml)
through Apteva's Apps page. Source entry: `github.com/apteva/apps`,
`mcp/neural`, pinned to `neural/v0.1.1`. For local installation testing, use Apteva's source installer
with a local git snapshot. The same installer builds the sidecar, wires its
SQLite database, serves its panel, registers MCP tools, and schedules training.

## First experiment

1. Open the project's **Neural** page and create a network.
2. Press **Train network**. Watch activations, weights, and the decision map.
3. Enable **Cycle training inputs** to watch different neurons fire.
4. Probe the map or move X/Y, and inspect a neuron's spike trace.
5. Save a version and use **Versions & endpoints** to deploy it.

The spike view uses leaky integrate-and-fire encoding of actual activation
magnitudes. It visualizes the activity of a dense network; the training
algorithm is ordinary backpropagation, not spiking-network training.
Animation can be disabled and respects the initial reduced-motion preference.

## v0.1 scope

- Real CPU training in the sidecar, with no external app dependencies.
- XOR quadrants, a circle, or a linear split. Reproducible seeds produce
  192 training points and 96 separate validation points.
- Two inputs, one or two hidden layers of 2–12 neurons, a sigmoid output,
  tanh hidden activations, full-batch Adam, and binary cross-entropy loss.
- Start, pause, resume, or step exactly one epoch. Persisted Adam moments
  allow exact continuation after restart. Training runs without an open panel.
- Immutable saved model versions and version-pinned, authenticated CPU
  prediction endpoints. Later training cannot change a deployed prediction.

SQLite tables are `experiments`, `model_versions`, and `deployments`.
Each experiment holds its fixed configuration, mutable network/optimizer
state, and bounded metric history. Every record is project-scoped.
The trainer advances up to four experiments by five epochs each second.
Limits: 100 experiments per project and 2,000 epochs per experiment.

Larger datasets, external Python workers, GPUs, and LLMs are future work.

## MCP and HTTP

- `experiments_create`, `experiments_list`, `experiments_get`
- `experiments_control` with `action: start | pause | step`
- `model_versions_create`, `model_versions_list`
- `deployments_create`, `deployments_list`
- `predictions_create` with exactly one of `experiment_id` or `deployment_id`,
  plus numeric `x` and `y` in [-1, 1]

The native panel uses POST `/api/apps/neural/rpc` with `{tool,args}`, carrying
the host-provided project and install IDs. Predictions are also available at
POST `/api/apps/neural/deployments/:id/predict` with `{"x":0.6,"y":0.4}`.
Use normal Apteva authentication. The result includes probability, class,
activations, epoch, and the actual saved version ID.

Tests cover numerical gradients, held-out learning, exact checkpoint
continuation, concurrent training and inspection, project isolation,
pause/step behavior, immutable deployed predictions, HTTP validation,
manifest/tool/panel consistency, and frontend activation/spike mathematics.

## Theme integration

The native panel inherits Apteva’s Terminal and Clean themes in light and dark modes. Surfaces, text, borders, controls, focus rings, chart colors, fonts, shadows, and corner radii use host CSS tokens. Canvas plots resolve the current tokens and redraw on theme changes, including while animation is paused. Custom themes use the same token contract. Class A uses squares and class B uses circles, so the decision map does not rely on color alone.

Network activity, loss curves, class markers, prediction values, and normal status badges use the active accent and a tonal variation mixed with the host text color. The Terminal/dev theme therefore uses oranges throughout; changing the accent recolors the entire panel. Negative weights and validation curves are dashed, and class markers retain distinct shapes. Error messages use the host error token.
