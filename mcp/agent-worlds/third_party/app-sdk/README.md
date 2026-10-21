# Bundled Apteva app SDK

This directory contains the unmodified production Go source used to build Agent Worlds 0.4.0. It keeps the tagged app independently buildable while the generic observation APIs are not yet part of a public SDK tag. SNAPSHOT.json records the source repository base and SHA-256 of every copied file. Tests and unrelated documentation are excluded.

Worlds imports the SDK normally; go.mod resolves that dependency to this directory. Do not add app behavior here. Replace the snapshot with a published SDK pin once that tag includes the observation APIs.
