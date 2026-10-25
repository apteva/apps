# Instances 0.14.1

Fixes resource-policy consistency across native prepare, inspect, start, stop
and delete requests. Previously a resource-enabled deployment could prepare
successfully then fail inspection with HTTP 409. Every lifecycle operation now
fingerprints the same typed policy; altered policies are still rejected.
Empty policies retain compatibility with deployments created before 0.14.0.

Adds a full resource-enabled lifecycle regression covering retries, successful
start/readiness, stop/deletion and rejection of changed policies, plus legacy
empty-policy coverage. Full Go and focused race suites pass.
