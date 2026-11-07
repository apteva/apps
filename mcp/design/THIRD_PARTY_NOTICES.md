# Design Studio geometry and viewer dependencies

The source and reproducible build scripts are in `runner/` and `ui/`.

- Replicad 0.23.1: https://github.com/sgenoud/replicad (MIT).
- replicad-opencascadejs 0.23.0: https://github.com/sgenoud/replicad-opencascadejs; Open CASCADE Technology with LGPL 2.1 and its additional exception. The pinned modelling WASM is embedded unchanged from the package.
- opencascade.js 1.1.1: https://github.com/donalffons/opencascade.js/tree/1.1.1 (LGPL 2.1); OCCT 7.4.0p1 import kernel. Its unmodified WASM is copied from the npm package. Source, binding generator and build instructions are included in that upstream repository and package. It runs as a separate import process.
- Three.js 0.180.0: https://github.com/mrdoob/three.js/tree/r180 (MIT).

Original manufacturer STEP assets retain their own licences and attribution. An enclosure's open-hardware licence does not replace manufacturer terms. Imported source metadata is preserved in manufacturing packages.
