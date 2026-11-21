# Database 0.19.0

Databases survive a runtime restored from a snapshot. The app SDK hands
the sidecar a restore context that maps the snapshot's project and app
installation ids to the new runtime's, and Database's restore hook moves
the scope keys in its engine control registry before the app mounts:
project databases move to the new project, an app's private databases
follow the owning app's new installation id, and access grants move with
their databases. Records are stored under database ids, which do not
change, so no record data is rewritten. Scopes of other projects in the
same install stay as they are.

The registry update records the restore id in the same transaction, so a
restart before the SDK records the restore cannot move scopes twice.

The app pins SDK v0.118.0. The AptevaDB dependency remains alpha.23.
