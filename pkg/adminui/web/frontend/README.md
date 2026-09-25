# Admin UI frontend

Svelte plus TypeScript, built with Vite. This is the source for the admin
UI shell, served by `internal/server` (`spa.go`) at `GET /ui/` on the admin
listener, alongside the existing dashboard at `GET /`.

## Build

From the repo root:

```
make ui-build
```

This runs `npm ci && npm run build` here, writing the static output to
`../dist` (`pkg/adminui/web/dist/`), then rebuilds the Go binary so the
freshly built assets are embedded via `pkg/adminui/web/embed.go`.

Node and npm are build-time only. The shipped server binary embeds the
built assets in `../dist`; nothing under this directory or its
`node_modules` is present in the binary or required at runtime.

## Develop

```
npm install
npm run dev
```

## Type check

```
npm run check
```

## Test

```
npm run test
```
