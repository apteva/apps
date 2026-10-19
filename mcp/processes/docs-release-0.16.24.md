# Processes 0.16.24

Extends runtime-error holds to a current worker’s tracked reminder event after the original assignment settled. Reminders are validated against the current worker and delivery epoch. Older or other-worker callbacks are ignored; original delivery identity and lifecycle sequence remain intact. Full Go suite covers original and reminder failures, explicit-repair epochs, later settlement, receipt preservation, and lifecycle authorization. Social and Core are unchanged.
