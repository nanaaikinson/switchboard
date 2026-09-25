# Route table config

The daemon stores routes in `routes.toml`.

## Location

The first match wins:

1. `$SWITCHBOARD_CONFIG_DIR/routes.toml`
2. Windows: `%APPDATA%\switchboard\routes.toml`
3. `$XDG_CONFIG_HOME/switchboard/routes.toml` (only if `XDG_CONFIG_HOME` is an absolute path)
4. `~/.config/switchboard/routes.toml` (macOS and Linux)

The directory is created with mode `0700` and the file with mode `0600`. Writes are
atomic: a temp file is written in the same directory and renamed over the old file.

## Format

```toml
schema_version = 1

[[routes]]
name = "myapp.test"
port = 7000
wildcard = false
redirect_https = true

[[routes]]
name = "tenants.myapp.test"
port = 3000
wildcard = true        # also matches *.tenants.myapp.test
redirect_https = true
```

| Key              | Type | Required | Meaning                                    |
| ---------------- | ---- | -------- | ------------------------------------------ |
| `schema_version` | int  | yes      | Config schema version. Currently `1`.      |
| `name`           | str  | yes      | Hostname. Unique, case-insensitive.        |
| `port`           | int  | yes      | Local upstream port, 1–65535.              |
| `wildcard`       | bool | no       | Also match subdomains of `name`.           |
| `redirect_https` | bool | no       | Redirect HTTP requests to HTTPS.           |

## Loading rules

- A missing file is an empty route table.
- Unknown keys are errors, so typos are caught instead of silently ignored.
- A missing `schema_version` is an error.
- A `schema_version` newer than this build supports is an error: upgrade `sb`.
- Older schema versions will be migrated forward, and the old file backed up, once a
  version 2 exists.
