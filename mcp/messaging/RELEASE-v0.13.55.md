# Messaging v0.13.55

The sender display-name field now uses the generic placeholder “Your company
or team”. Personal names and business references have also been replaced with
neutral examples in Messaging's comments and test fixtures. The production
panel bundle and source map have been rebuilt.

This release includes all Messaging changes through v0.13.54, including Gmail
and SES email providers, the web and native conversations widgets, sending
from the web widget, attachment handling, reliable message processing, email
threading, HTML-only email content, and the extended legacy-database startup
budget. It introduces no database migrations or changes to stored messages,
senders, credentials, or routing configuration.

The app SDK dependency is pinned to v0.91.0, the latest tag on its main branch.
Validation: Messaging Go tests and source build, UI tests, TypeScript checking,
production UI bundle verification, and a check that no personal example
references remain in Messaging's source or built assets.
