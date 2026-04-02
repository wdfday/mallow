// Package handler provides HTTP handlers for authentication operations.
//
// # Handler Organization
//
// The auth handler is organized into multiple files for better maintainability:
//
//   - router.go: Facade wiring Auth/Password/Verify sub-handlers
//   - auth_handler.go: Core authentication operations (register, login, token refresh, OAuth)
//   - password_handler.go: Password management (change, forgot, reset)
//   - verify_handler.go: Email verification operations
//
// # Route Registration
//
// Each handler file exposes its own route registration method:
//
//   - AuthHandler.RegisterRoutes(): Registers authentication routes
//   - PasswordHandler.RegisterRoutes(): Registers password management routes
//   - VerifyHandler.RegisterRoutes(): Registers email verification routes
//
// The facade Handler in router.go delegates to these specific registration
// methods to keep routes organized by functionality.
//
// # Usage
//
// The handler is registered via FX dependency injection and routes are
// automatically configured during application startup:
//
//	handler := handler.NewHandler(authService, passwordService, verificationService, cfg)
//	handler.RegisterRoutes(engine, authMiddleware)
//
// # Route Structure
//
// All routes are under /api/v1/auth with both public and protected endpoints:
//
// Public routes:
//   - POST /register
//   - POST /login
//   - POST /refresh
//   - POST /google
//   - POST /verify-email
//   - POST /resend-verification
//   - POST /forgot-password
//   - POST /reset-password
//
// Protected routes (require authentication):
//   - GET /me
//   - POST /change-password
//   - POST /send-verification
package handler
