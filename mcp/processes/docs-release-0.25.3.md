# Processes 0.25.3

On All projects Home, each row of the Processes widget shows its project next
to the title: a colour dot from the project's Settings colour, then the
project name.

- The colour comes from the dashboard's `widgetContext.projects`. Only
  `#rgb` / `#rrggbb` values are used, as an inline style.
- No colour, or a dashboard that doesn't send project colours: the name
  shows alone, as before.
- The tag only appears when rows can come from several projects (All
  projects, no project filter). Inside a project nothing changes.
- The project name no longer repeats in the row's summary line.
