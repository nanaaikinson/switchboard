# Project config: switchboard.toml

A `switchboard.toml` in a repository lists the routes that project needs, so everyone
who clones it gets the same names with one command:

```bash
sb init      # writes a commented example
sb apply     # adds or updates the routes; run it again after every edit
sb apply --down
```

## Format

```toml
[routes]
# "name" = port on 127.0.0.1
"shop" = 3000

# A table sets options. redirect = false serves plain HTTP too, instead of
# redirecting it to HTTPS.
"api.shop" = { port = 4000, redirect = false }

# A name starting with "*." also matches every subdomain.
"*.tenants.shop" = 3000
```

| Form | Meaning |
| --- | --- |
| `"name" = 3000` | route `name` to `127.0.0.1:3000`, redirecting HTTP to HTTPS |
| `"name" = { port = 3000 }` | the same |
| `"name" = { port = 3000, redirect = false }` | no HTTP-to-HTTPS redirect |

- **Names** follow the same rules as `sb add`. Names without a Switchboard TLD get the
  default one (`shop` → `shop.test`). Quote names that contain dots or `*`.
- **Strict parsing.** Only `[routes]` is allowed at the top level, and only `port` and
  `redirect` inside a route. Anything else is an error naming the key, so typos are
  caught. So are ports outside 1–65535, and ports written as strings. `sb apply`
  refuses ports 80 and 443, where Switchboard itself listens.
- **Duplicates.** Two entries that mean the same name (`"shop"` and `"shop.test"`) are
  an error.

## Which file `sb apply` uses

- `sb apply` with no argument uses the nearest `switchboard.toml` in the current
  directory or its parents, stopping at the git repository root. The root is the first
  directory with a `.git` directory or file, so worktrees and submodules count. Outside a
  git repository, only the current directory is searched, so a stray file in a parent
  directory is never picked up.
- `sb apply <path>` uses that file, or `<path>/switchboard.toml` if the path is a
  directory.

## How applying works

Every route from a file is tagged with the file's absolute path, with symlinks in its
directory resolved. The tag is stored in `routes.toml` as `file = "…"` (see
[config.md](config.md)). The API shows these routes as `"source": "file"`, with the
path in `"file"`. `sb ls` shows `file (<path>)`.

`sb apply` makes that file's routes match the file:

- **Added** (`+`) and **updated** (`~`): routes new to the file, or whose port or
  redirect changed.
- **Removed** (`-`): routes that were applied from this file before but aren't listed
  any more.
- **Unchanged:** nothing happens. Applying an unedited file writes nothing, sends no
  route events, and prints `Up to date`. That makes it safe in scripts, git hooks or
  `make dev`.
- **Other sources are never touched.** Routes added with `sb add`, routes from another
  project's file, and Docker routes keep their settings.

`sb apply --down` removes every route tagged with the file and nothing else. It works
after the file has been deleted: pass its path.

## Conflicts

If a name in the file is already taken by another source, that route is skipped with a
warning, and the other route is left as it is:

```
warning: skipped admin.shop.test: already taken by a route added with 'sb add'; rename it in switchboard.toml, or remove the other route
```

"Taken" means the same exact name, or the same wildcard. A `*.x` wildcard conflicts
with a route named `x` that has `wildcard = true`. An exact name *under* someone
else's wildcard (`a.x` when `*.x` exists) isn't a conflict, because exact names always
win over wildcards. The warning repeats on every `sb apply` until the conflict is
resolved. `sb apply` still exits 0 when there are conflicts.

Owners reported in conflicts:

| Owner | How to free the name |
| --- | --- |
| a route added with `sb add` | `sb rm <name>` |
| a route from another `switchboard.toml` | edit that file and `sb apply` it, or `sb apply --down <file>` |
| a Docker container | stop it, or label it `dev.switchboard.enable=false` |

`sb add` on a name that a file owns replaces the route with a hand-made one, which
drops the file tag. The next `sb apply` of that file then reports it as a conflict.
