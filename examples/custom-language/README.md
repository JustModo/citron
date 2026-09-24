# Custom language

Adds Bash to the standard image without changing citron.

```sh
docker build -t citron:latest .
docker build -t citron:bash examples/custom-language
docker run --rm --entrypoint citron citron:bash -config /opt/citron/configs/citron.conf -languages
```

Shipped packs that the default image leaves out (`go`, `rust`, `javascript`,
`typescript`) are selected at build time instead:

```sh
docker build --build-arg LANGUAGES="c cpp java python go rust" -t citron:latest .
```

## Pack format

| File | Purpose |
|---|---|
| `language.toml` | Id, name, source filename, compile and run commands, limits |
| `packages.txt` | Optional. apt packages to install, one per line |
| `install.sh` | Optional. Runs as root from the installed pack directory after the packages; for setup apt cannot do |
| other files | Optional. Scripts referenced from `language.toml` as `{{.Pack}}/...` |

`language.toml` keys:

| Key | Purpose |
|---|---|
| `id`, `name`, `label` | API identifiers; `id` and `name` must be unique |
| `source`, `binary` | Filenames of the submitted source and the compiled artifact |
| `compile`, `run`, `probe` | Commands. `compile` is optional; `probe` prints the toolchain version at startup |
| `mounts` | Absolute paths outside `/usr` the toolchain reads |
| `env` | Extra `KEY=VALUE` environment for compile and run |
| `[limits]`, `[compile_limits]` | `memory_extra_mb`, `max_processes`, `wall_multiplier`, `cpu_multiplier` applied to the configured limits |

Commands and pack scripts run inside the sandbox as an unprivileged user, like the
submitted program. Templates can use `{{.Source}}`, `{{.Binary}}`, `{{.Pack}}`,
`{{.StackKB}}`, `{{.HeapMB}}` and `{{.MemMB}}`.

Conventions:

- Put toolchain binaries on `/usr/local/bin:/usr/bin:/bin`, symlinking from
  `install.sh` if they live elsewhere; list other paths they read in `mounts`.
- Pin versions of anything `install.sh` downloads.
- Runtimes that start a thread per CPU core (Go, the JVM) should be limited to one,
  since threads count against `max_processes`.

[languages/java](../../languages/java/) shows a compile script and
[languages/go](../../languages/go/) a build-time `install.sh`.
