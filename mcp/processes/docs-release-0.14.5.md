# Processes 0.14.5

This release adds global Home support for the Processes overview widget. The
widget can now be placed on Home → All projects when Processes is installed at
global scope, while the existing project Home behavior remains unchanged.

Global overview is read-only. It asks the platform for projects visible to the
caller, aggregates only those projects from the global installation's own
database, annotates every run, schedule, attention item, and live step with its
project ID and name, and supports filtering to one visible project. Deep links
retain the originating project context. Project-scoped installations keep their
separate storage and history; they are not implicitly copied into the global
installation.

The release also includes regression coverage for hidden-project exclusion,
global filtering, project-aware links, and the production panel bundle.
