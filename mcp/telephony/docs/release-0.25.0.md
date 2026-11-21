# Telephony 0.25.0

- Browser audio works for a Telephony copy running inside an Environment.
  The install is not on the platform's app gateway there: a test browser
  reaches it through the environment's loopback gateway,
  `/api/environment-app-public/<environment>`. Telephony now returns call and
  listener audio paths (`media_url`) under that prefix when it runs in an
  Environment (`APTEVA_ENVIRONMENT_ID` set). Test harnesses no longer need to
  rewrite the browser's WebSocket URL.
- The browser client accepts that one prefix in front of the install path and
  still refuses any other server, install, call or path.
- Outside Environments nothing changes: paths stay
  `/api/apps/telephony/_install/<id>/softphone/...`.
- The dashboard Calls panel bundle is unchanged in this release.
