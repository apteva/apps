# Design Studio changes

## 0.6.0

- Add an optional `fabrication_provider` integration role (multiple connections). Bind any of Craftcloud, Slant 3D, Shapeways, Treatstock and Sculpteo; each has an adapter behind one `FabricationProvider` interface (quote, prepare, submit, status, cancel).
- `fabrication_quote` builds STL exports for the printed parts of a passing revision, sends only those files to every bound service in parallel, and stores options ranked by total. Provider failures are recorded per connection without failing the quote.
- Orders are drafts first. `fabrication_order_prepare` re-prices the option at the provider and checks the operator's policy: auto-order switch, per-order maximum, monthly budget, allowed services, currency, and price drift over 5% since quoting. Outside the policy the order waits for a person.
- Approval, decline and the policy/addresses settings exist only as panel HTTP routes, never as MCP tools. Approval must restate the exact total and records the signed platform principal when present.
- `fabrication_order_submit` (new `fabrication.order` permission) moves an approved order to submitting atomically and calls the provider once. An ambiguous provider error marks the order `submit_unknown` instead of retrying.
- Pin app-sdk v0.116.0. A `fabrication-status` worker polls active orders every 30 minutes. The panel gains a Fabrication tab with quotes, orders and approval, and policy/address/service settings.

Craftcloud's public v5 API has no model upload endpoint, so live Craftcloud quotes depend on the integration connector's upload path. Craftcloud orders end in `awaiting_payment` unless invoice payment is configured. Treatstock has no status API; Sculpteo orders hold at most three parts.

## 0.5.0

- Import original manufacturer STEP as immutable, project-scoped source assets with XDE component names, hierarchy, placements, declared units, measured bounds, attribution and dimensional evidence. Normalize origins/orientation explicitly. Keep imported purchased parts out of manufacturing outputs.
- Check solid intersections, containment, exact surface distances, engineering clearances and explicit contact/ignored pairs. Return closest points and indeterminate results for unsupported geometry. Add owner-relative keepouts and conservative insertion/removal envelopes.
- Inspect assemblies with depth-rendered selection, component visibility, isolation, transparency, section planes, standard views, fit selection, exact instance/face measurements and clearly labelled approximate point measurements. Link validation issues to affected geometry.
- Generate editable multi-board enclosure templates without PCB Studio: front shell, rear cover, lip, measured display opening/retention features, screw bosses/bores, supports, brackets, cradle and service/ventilation cutouts. Add numeric and datum placement plus printer/material fit profiles.
- Separate declared FDM rules from sampled geometry checks for wall thickness, feature spans and overhangs. Sampled violations identify regions; a clean sample remains indeterminate.
- Export unique oriented manufacturing parts separately from reference assembly STEP/GLB. Archive resolved source dependencies, original/normalized assets, checksums, validation, evidence, BOM and depth-rendered orthographic inspection images with a database-independent replay tool.
- Gate verified packages on complete evidence, named passing checks, resolved assumptions, instructions and revision-bound physical-fit/thermal test records. Prototype exports remain explicitly identified. Fix hidden instances being omitted from physical validation.
- Pin app-sdk v0.115.0. Include pinned import WASM, dependency notices and licence files. Increase the geometry timeout default to 300 seconds and memory limit to 3 GiB for manufacturer assemblies.

Manufacturer STEP is a geometric reference, not original editable feature history. Non-ASCII imported names currently use an ASCII fallback. Flexible routing, RF/thermal simulation, automatic constraint solving, complete thin-feature recognition and slicer certification are outside this release. Physical measurements and tests are still required before Pocket's enclosure can be verified.
