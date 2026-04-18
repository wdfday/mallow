# Identity Service

Identity là module quản lý danh tính người dùng cho hệ thống microservice:
- Authentication: register/login/logout/refresh
- Authorization context: JWT claims + role (`user`/`admin`)
- User/Profile cơ bản
- Linked account (Telegram)
- Email workflow bất đồng bộ qua NATS

## Tech Stack
- Go `1.26`
- Gin + Fx
- PostgreSQL (GORM)
- Redis
- NATS / JetStream

## Features
- Email/password auth
- Google auth endpoint (nếu bật provider ở service layer)
- Refresh token qua HTTP-only cookie
- Account lockout / rate limit middleware
- Admin routes quản lý user
- Profile `me` endpoint
- Telegram link/unlink
- JWKS + OpenID well-known endpoints
- Swagger UI + auto-generate OpenAPI

## Project Layout
- `cmd/identity/`: entrypoint
- `internal/module/auth`: auth module
- `internal/module/user`: user + admin manage
- `internal/module/profile`: profile module
- `internal/module/notification`: email workflow
- `internal/router`: route wiring
- `internal/infra`: db/redis/nats providers
- `docs/`: generated swagger artifacts

## API Docs (Swagger)
Khi service chạy local:
- `http://localhost:8080/swagger/index.html`
- `http://localhost:8080/api/v1/swagger/index.html`
- OpenAPI JSON: `http://localhost:8080/api/v1/swagger/doc.json`

Regenerate docs:
```bash
cd identity
just swagger
```

## Quick Start (Local)
### 1) Start infrastructure
```bash
docker compose -f identity/docker-compose.yml up -d
```

### 2) Run service
```bash
cd identity
just run
```

### 3) Dev hot reload
```bash
cd identity
just dev
```
(requires `air` installed)

## Just Commands
```bash
cd identity
just --list
```
Main commands:
- `just run`: run service
- `just dev`: run with air
- `just test`: run tests
- `just swagger`: regenerate swagger docs
- `just swagger-check`: ensure generated docs are up-to-date

## Database Behavior
Service auto-creates/updates tables at startup via `AutoMigrate`:
- `users`
- `user_profiles`

Additional index is also created:
- `idx_users_linked_accounts` (GIN on `users.linked_accounts`)

## Environment Variables
Config loads from `.env` + environment variables.
Most important keys:
- `SERVER_PORT`
- `POSTGRES_URL`
- `REDIS_URL`
- `NATS_URL`
- `JWT_SECRET` or `JWT_PRIVATE_KEY`/`JWT_PUBLIC_KEY`
- `JWT_ISSUER`
- `SERVICE_SECRET` (internal service auth)
- `CORS_ORIGINS`
- `ADMIN_SEED_EMAIL`, `ADMIN_SEED_PASSWORD`

Reference file: `identity/.env`

## Core Routes
Public:
- `POST /api/v1/auth/register`
- `POST /api/v1/auth/login`
- `POST /api/v1/auth/refresh`
- `POST /api/v1/auth/forgot-password`
- `POST /api/v1/auth/reset-password`
- `POST /api/v1/auth/verify-email`
- `POST /api/v1/auth/resend-verification`
- `GET /health`
- `GET /.well-known/openid-configuration`
- `GET /.well-known/jwks.json`

Protected (JWT):
- `POST /api/v1/auth/logout`
- `POST /api/v1/auth/change-password`
- `POST /api/v1/auth/send-verification`
- `GET /api/v1/user/me`
- `PUT /api/v1/user/me`
- `GET /api/v1/profile/me`
- `PUT /api/v1/profile/me`

Admin only:
- `GET /api/v1/user/manage`
- `PATCH /api/v1/user/manage/:id/suspend`
- `PATCH /api/v1/user/manage/:id/reinstate`
- `PATCH /api/v1/user/manage/:id/role`

## Reuse Notes (When splitting to standalone repo)
- Keep `pkg/shared` dependency strategy explicit:
  - either vendor/copy shared package,
  - or publish shared package as separate module and update imports.
- Keep `.air.toml`, `justfile`, and `docs/` in repo for dev velocity.
- Preserve env contract to avoid breaking gateway integration.
- If gateway verifies JWT using JWKS, ensure `JWT_ISSUER` and `/.well-known/jwks.json` are stable.
