# Computer v0.7.89

Native image uploads now work when a visible Browse control opens a hidden
or dynamically created file input that cannot be identified from nearby DOM
elements. Ordinary direct-input uploads retain their existing fast path.

- Use the browser's intercepted file-chooser event to identify the exact
  upload input; never guess between multiple hidden inputs. This is shared
  browser logic with no Patreon-specific runtime selectors.
- Reject ambiguous upload selectors, stale targets, missing labels, and
  unsuitable chooser controls. Preserve click-only argument restrictions.
- Give upload-specific recovery instructions: refresh the screenshot and
  retry `upload_file` with its current target, revision, and original source.
  Document `expected_name` and copying only explicitly reported role values.
- Handle upload inputs cleared by page change handlers and reject inputs that
  disappear before assignment. Clarify still-image preview verification to
  avoid waiting for an audio/video player signal.
- Pin app-sdk v0.76.0, the latest tag by commit topology.

## Regression coverage

- New tier 3 native Patreon image upload: a real model selects Browse, uploads
  one PNG from a URL, verifies a saved draft, and checks image persistence
  after reload on a disposable creator.
- New tier 3 chooser and recovery fixtures: dynamically created input,
  decoded image dimensions, stale revision and unsupported argument errors,
  model-requested screenshot refresh, and exactly one successful upload.
- Deterministic tests prove rejected arguments do not download or dispatch
  files; a new screenshot and supported name guard allow a valid retry.
- Browser regressions cover paths and payloads, static and dynamic inputs,
  unrelated inputs, clearing/removal, ambiguous selectors, and target guards.
- Include the previously validated Bunny-video scheduled-publication test,
  which verifies automatic publication at the deadline and persistence after
  reload. This is distinct from native image uploading.

The LLM harness retries inference once only if no decision file was produced;
it never retries an already-dispatched browser action automatically.
