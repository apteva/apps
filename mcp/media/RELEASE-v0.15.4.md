# Media v0.15.4

Pin Media Executor v0.15.1 to immutable source commit
`6b563e28573665d8f29eb59788163f7f706e183a`. The worker now exposes authenticated
runtime status and validates Server's deployment identity and generation on
activation and drain, allowing current Server releases to confirm admission
before routing component calls.

Includes the v0.15.3 audio-normalization presentation fix and all existing video,
timestamp, loudness, peak and transfer checks. Media still has no Instances
dependency; Server owns optional worker placement. SDK remains v0.110.0.

Validated with worker race tests and real parent/worker SDK tests, including
status authentication, generation rejection, durable jobs, remote transfer and
confirmed cancellation. Production verification uses disposable synthetic media;
existing masters and session runs are not rerendered or resumed.
