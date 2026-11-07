# Design Studio changes

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
