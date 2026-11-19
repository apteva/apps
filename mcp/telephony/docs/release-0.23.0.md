# Telephony 0.23.0

- A number can be set up with no agent when only people answer it. A
  platform caller (API key, panel, or another app such as Tests) creates the
  route with `telephony_routes_create` and `answer_mode: "human_browser"`
  without `agent_id`. Before, every route needed an owning agent, so a
  platform caller with no agent to name could not create one.
  - Such a number stays with people: switching it to `agent` or
    `realtime_immediate` answering is refused, from the tool and from the
    panel, because nobody would be told about the call or there would be no
    agent to run it. A flow can still send its callers to AI, because saved AI
    and agent destinations always name their own agent.
  - It is managed by the platform: agents cannot see or change it.
  - The panel offers **No agent** when a number rings in the browser, and
    shows routed numbers with no agent as such.
- A call with no agent queues no incoming-call event. The event goes to the
  call's agent, and the platform refuses agent 0, so it would be retried every
  5 minutes until the call was deleted at retention.
