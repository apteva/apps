# Telephony 0.32.0

- Routing decision nodes and AI destination context lookups accept an optional
  `function_stage`. It is passed to `functions_invoke` as `stage`, so a flow can
  run the `staging` or `production` stage of one function (its promoted version,
  vars and access). Without it nothing changes: Functions runs the function's
  active version.
- Validation: the stage must match Functions' stage names
  (`^[a-z0-9][a-z0-9-]{0,39}$`); flows and destinations with another value are
  rejected when saved.
