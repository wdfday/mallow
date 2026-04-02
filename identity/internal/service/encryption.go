package service

import "mallow/pkg/encryption"

// EncryptionService is kept for backward compatibility and delegates to mallow/pkg.
type EncryptionService = encryption.EncryptionService

// NewEncryptionService creates an AES-256-GCM encryption service.
var NewEncryptionService = encryption.NewEncryptionService
