---
description: Review vnkvm lessons, task records and recipes, and revise the rules that slowed tasks down
---

Review how vnkvm work has gone and revise the lessons. Follow "Improve with every task" in the vnkvm skill.

1. Call the `lessons` tool without `app`. It lists every app with lessons, task records or recipes, and marks the apps whose review is due.
2. Review app `all` first, then each app with `review_due`, then any app the person names. For each, call `lessons` with the app and compare `by_version` and `recent_tasks`:
   - Rewrite or drop lines tagged with a version that made tasks slower. Keep lines from versions that made them faster. Merge duplicates.
   - A rule that holds in several apps moves to `all`.
   - Save each app with `set`, keeping the tag of every line kept unchanged.
3. Call `recipes` with each app. Recipes that fail on most runs: delete them, or tell the person which to redo.
4. Report briefly, per app: what changed, the numbers behind it, and what the next tasks should show. Do not record anything personal or read from a window.
