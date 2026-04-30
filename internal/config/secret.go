package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/scrypt"
)

const hiddenSecretPrefix = "koios-hide:v1:"

const (
	hiddenSecretSaltBytes  = 32
	hiddenSecretNonceBytes = 12
	hiddenSecretKeyBytes   = 32
	hiddenSecretScryptN    = 1 << 15
	hiddenSecretScryptR    = 8
	hiddenSecretScryptP    = 1
)

const (
	hiddenSecretPathTelegramBotToken      = "channels.telegram.bot_token"
	hiddenSecretPathTelegramWebhookSecret = "channels.telegram.webhook_secret"
	hiddenSecretPathHooksWebhookSecret    = "hooks.webhook_secret"
	hiddenSecretPathHooksWebhookToken     = "hooks.webhook_token"
)

// Exported field-path constants for use by CLI commands.
const (
	HiddenSecretPathTelegramBotToken      = hiddenSecretPathTelegramBotToken
	HiddenSecretPathTelegramWebhookSecret = hiddenSecretPathTelegramWebhookSecret
	HiddenSecretPathHooksWebhookSecret    = hiddenSecretPathHooksWebhookSecret
	HiddenSecretPathHooksWebhookToken     = hiddenSecretPathHooksWebhookToken
)

// SetHiddenSecret updates the internal hidden-secrets cache so that the next
// call to EncodeTOML writes the new hidden blob for the given field path.
// It does not modify any Config field directly.
func SetHiddenSecret(cfg *Config, path, hidden string) {
	cfg.setHiddenSecret(path, hidden)
}

var (
	hiddenSecretHostname                = os.Hostname
	hiddenSecretUserCurrent             = user.Current
	hiddenSecretUserConfigDir           = os.UserConfigDir
	hiddenSecretReadFile                = os.ReadFile
	hiddenSecretMkdirAll                = os.MkdirAll
	hiddenSecretRandReader    io.Reader = rand.Reader
)

type hiddenSecretFingerprint struct {
	keyID  string
	digest []byte
}

func IsHiddenSecret(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), hiddenSecretPrefix)
}

func HideSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", errors.New("secret must not be empty")
	}
	fingerprint, err := currentHiddenSecretFingerprint()
	if err != nil {
		return "", err
	}
	masterKey, err := loadHiddenSecretMasterKey(fingerprint.keyID, true)
	if err != nil {
		return "", err
	}
	salt := make([]byte, hiddenSecretSaltBytes)
	if _, err := io.ReadFull(hiddenSecretRandReader, salt); err != nil {
		return "", fmt.Errorf("generate secret salt: %w", err)
	}
	nonce := make([]byte, hiddenSecretNonceBytes)
	if _, err := io.ReadFull(hiddenSecretRandReader, nonce); err != nil {
		return "", fmt.Errorf("generate secret nonce: %w", err)
	}
	derivedKey, err := deriveHiddenSecretKey(masterKey, fingerprint.digest, salt)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return "", fmt.Errorf("build cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("build gcm: %w", err)
	}
	aad := hiddenSecretAAD(fingerprint.keyID)
	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), aad)
	return hiddenSecretPrefix + strings.Join([]string{
		fingerprint.keyID,
		base64.RawURLEncoding.EncodeToString(salt),
		base64.RawURLEncoding.EncodeToString(nonce),
		base64.RawURLEncoding.EncodeToString(ciphertext),
	}, "."), nil
}

func RevealSecret(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if !IsHiddenSecret(trimmed) {
		return value, nil
	}
	parts := strings.Split(strings.TrimPrefix(trimmed, hiddenSecretPrefix), ".")
	if len(parts) != 4 {
		return "", errors.New("invalid hidden secret format")
	}
	fingerprint, err := currentHiddenSecretFingerprint()
	if err != nil {
		return "", err
	}
	if parts[0] != fingerprint.keyID {
		return "", fmt.Errorf("hidden secret is bound to a different host/user fingerprint")
	}
	masterKey, err := loadHiddenSecretMasterKey(fingerprint.keyID, false)
	if err != nil {
		return "", err
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode secret salt: %w", err)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("decode secret nonce: %w", err)
	}
	if len(nonce) != hiddenSecretNonceBytes {
		return "", errors.New("invalid hidden secret nonce length")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return "", fmt.Errorf("decode secret ciphertext: %w", err)
	}
	derivedKey, err := deriveHiddenSecretKey(masterKey, fingerprint.digest, salt)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return "", fmt.Errorf("build cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("build gcm: %w", err)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, hiddenSecretAAD(fingerprint.keyID))
	if err != nil {
		return "", fmt.Errorf("decrypt hidden secret: %w", err)
	}
	return string(plaintext), nil
}

func encodeHiddenSecretLiteral(cfg *Config, path, plaintext string) string {
	if hidden := cfg.hiddenSecret(path); hidden != "" {
		return strconvQuoteHiddenSecret(hidden)
	}
	return strconvQuoteHiddenSecret(plaintext)
}

func decodeHiddenSecrets(dst *Config, src *fileConfig) error {
	if dst == nil || src == nil {
		return nil
	}
	for i := range src.LLM.Profiles {
		path := hiddenSecretPathModelProfileAPIKey(src.LLM.Profiles[i].Name)
		plaintext, raw, err := decodeHiddenSecretValue(src.LLM.Profiles[i].APIKey)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		src.LLM.Profiles[i].APIKey = plaintext
		dst.setHiddenSecret(path, raw)
	}
	telegram := &src.Channels.Telegram
	if plaintext, raw, err := decodeHiddenSecretValue(telegram.BotToken); err != nil {
		return fmt.Errorf("%s: %w", hiddenSecretPathTelegramBotToken, err)
	} else {
		telegram.BotToken = plaintext
		dst.setHiddenSecret(hiddenSecretPathTelegramBotToken, raw)
	}
	if plaintext, raw, err := decodeHiddenSecretValue(telegram.WebhookSecret); err != nil {
		return fmt.Errorf("%s: %w", hiddenSecretPathTelegramWebhookSecret, err)
	} else {
		telegram.WebhookSecret = plaintext
		dst.setHiddenSecret(hiddenSecretPathTelegramWebhookSecret, raw)
	}
	hooks := &src.Hooks
	if plaintext, raw, err := decodeHiddenSecretValue(hooks.WebhookSecret); err != nil {
		return fmt.Errorf("%s: %w", hiddenSecretPathHooksWebhookSecret, err)
	} else {
		hooks.WebhookSecret = plaintext
		dst.setHiddenSecret(hiddenSecretPathHooksWebhookSecret, raw)
	}
	if plaintext, raw, err := decodeHiddenSecretValue(hooks.WebhookToken); err != nil {
		return fmt.Errorf("%s: %w", hiddenSecretPathHooksWebhookToken, err)
	} else {
		hooks.WebhookToken = plaintext
		dst.setHiddenSecret(hiddenSecretPathHooksWebhookToken, raw)
	}
	return nil
}

func decodeHiddenSecretValue(value string) (plaintext string, raw string, err error) {
	trimmed := strings.TrimSpace(value)
	if !IsHiddenSecret(trimmed) {
		return value, "", nil
	}
	plaintext, err = RevealSecret(trimmed)
	if err != nil {
		return "", "", err
	}
	return plaintext, trimmed, nil
}

func hiddenSecretPathModelProfileAPIKey(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "llm.profiles.api_key"
	}
	return "llm.profiles." + trimmed + ".api_key"
}

// HiddenSecretFieldPathModelProfileAPIKey returns the canonical hidden-secret
// path for the named LLM profile's api_key field.
func HiddenSecretFieldPathModelProfileAPIKey(name string) string {
	return hiddenSecretPathModelProfileAPIKey(name)
}

func hiddenSecretAAD(keyID string) []byte {
	return []byte(hiddenSecretPrefix + keyID)
}

func deriveHiddenSecretKey(masterKey, fingerprintDigest, salt []byte) ([]byte, error) {
	kdfSalt := make([]byte, 0, len(fingerprintDigest)+len(salt))
	kdfSalt = append(kdfSalt, fingerprintDigest...)
	kdfSalt = append(kdfSalt, salt...)
	derivedKey, err := scrypt.Key(masterKey, kdfSalt, hiddenSecretScryptN, hiddenSecretScryptR, hiddenSecretScryptP, hiddenSecretKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("derive hidden secret key: %w", err)
	}
	return derivedKey, nil
}

func currentHiddenSecretFingerprint() (hiddenSecretFingerprint, error) {
	currentUser, err := hiddenSecretUserCurrent()
	if err != nil {
		return hiddenSecretFingerprint{}, fmt.Errorf("resolve current user: %w", err)
	}
	hostname, err := hiddenSecretHostname()
	if err != nil {
		return hiddenSecretFingerprint{}, fmt.Errorf("resolve hostname: %w", err)
	}
	machineID := readMachineID()
	material := strings.Join([]string{
		"hostname=" + strings.TrimSpace(strings.ToLower(hostname)),
		"username=" + strings.TrimSpace(currentUser.Username),
		"uid=" + currentUser.Uid,
		"gid=" + currentUser.Gid,
		"home=" + filepath.Clean(currentUser.HomeDir),
		"machine_id=" + machineID,
	}, "\n")
	digest := sha512.Sum512([]byte(material))
	keyID := hex.EncodeToString(digest[:32])
	return hiddenSecretFingerprint{keyID: keyID, digest: digest[:]}, nil
}

func readMachineID() string {
	for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		data, err := hiddenSecretReadFile(path)
		if err != nil {
			continue
		}
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}
	return ""
}

func loadHiddenSecretMasterKey(keyID string, create bool) ([]byte, error) {
	path, err := hiddenSecretKeyPath(keyID)
	if err != nil {
		return nil, err
	}
	key, err := hiddenSecretReadFile(path)
	if err == nil {
		if len(key) != hiddenSecretKeyBytes {
			return nil, fmt.Errorf("invalid hidden secret key size in %s", path)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read hidden secret key %s: %w", path, err)
	}
	if !create {
		return nil, fmt.Errorf("hidden secret key %s not found", path)
	}
	if err := hiddenSecretMkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create hidden secret key directory: %w", err)
	}
	key = make([]byte, hiddenSecretKeyBytes)
	if _, err := io.ReadFull(hiddenSecretRandReader, key); err != nil {
		return nil, fmt.Errorf("generate hidden secret key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return loadHiddenSecretMasterKey(keyID, false)
		}
		return nil, fmt.Errorf("create hidden secret key %s: %w", path, err)
	}
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write hidden secret key %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("close hidden secret key %s: %w", path, err)
	}
	return key, nil
}

func hiddenSecretKeyPath(keyID string) (string, error) {
	base, err := hiddenSecretUserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(base, "koios", "secrets", keyID+".key"), nil
}

func strconvQuoteHiddenSecret(value string) string {
	return fmt.Sprintf("%q", value)
}
