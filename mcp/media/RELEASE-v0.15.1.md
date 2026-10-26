# Media v0.15.1

Component upload chunks now use base64 on the public worker-to-parent hop. Public
gateways may reject raw binary media before it reaches Media, as reproduced with
a validated WAV output on DigitalOcean through the local zrok tunnel.

The parent streams decoded bytes to Storage. Existing per-job upload ownership,
project scope, transfer byte limits and final checksum validation remain enforced.
Direct Storage and S3 uploads retain their existing format. The executor component
remains v0.15.0, pinned to its tested source commit; no worker redeploy is needed.

Validated with real worker render/index/guarded-audio integration, encoded chunk
integrity and parallel uploads, transfer scope/expiry tests, and the Media suite.
