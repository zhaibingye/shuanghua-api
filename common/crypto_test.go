package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncryptSecretRoundTripAndLegacyCompatibility(t *testing.T) {
	originalSecret := CryptoSecret
	t.Cleanup(func() { CryptoSecret = originalSecret })
	CryptoSecret = "test-crypto-secret"

	ciphertext, err := EncryptSecret("moderation-api-key")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(ciphertext, encryptedSecretPrefix))
	assert.NotEqual(t, "moderation-api-key", ciphertext)

	plaintext, err := DecryptSecret(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, "moderation-api-key", plaintext)

	legacy, err := DecryptSecret("legacy-api-key")
	require.NoError(t, err)
	assert.Equal(t, "legacy-api-key", legacy)
}
