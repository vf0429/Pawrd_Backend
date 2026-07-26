package hicustom

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
)

// Authenticator holds the AppKey/AppSecret and caches the access_token.
// Signature algorithm follows the documented-but-unverified design (§7.2) and
// MUST be confirmed against HiCustom's official docs.
type Authenticator struct {
	appKey    string
	appSecret string

	mu        sync.RWMutex
	token     string
	expiresAt time.Time

	// fetchToken is overridable for testing. In production it calls the official
	// "获取 access_token" endpoint. nil => no remote fetch (token stays empty,
	// which is fine for the mock client that doesn't sign real requests).
	fetchToken func() (token string, ttl time.Duration, err error)
}

func NewAuthenticator(cfg *config.Config) (*Authenticator, error) {
	if err := cfg.ValidateHiCustomConfig(); err != nil {
		return nil, err
	}
	return &Authenticator{
		appKey:    cfg.HiCustomAppKey,
		appSecret: cfg.HiCustomAppSecret,
	}, nil
}

// Sign builds the MD5-uppercase signature over params sorted by key, with
// "&key=AppSecret" appended. TODO: confirm append style + URL encoding with
// the official docs (some platforms concatenate AppSecret directly).
func (a *Authenticator) Sign(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s&", k, params[k])
	}
	b.WriteString("key=" + a.appSecret)

	sum := md5.Sum([]byte(b.String()))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// CommonParams returns the per-request public parameters.
func (a *Authenticator) CommonParams() map[string]string {
	return map[string]string{
		"app_key":   a.appKey,
		"timestamp": fmt.Sprintf("%d", time.Now().Unix()),
		"nonce":     fmt.Sprintf("%d", time.Now().UnixNano()), // TODO: UUID if official requires
	}
}

// AccessToken returns a valid token, refreshing before the 5-minute safety
// window. Thread-safe. With fetchToken nil (mock / not yet wired) it returns "".
func (a *Authenticator) AccessToken() (string, error) {
	a.mu.RLock()
	if a.token != "" && time.Now().Add(5*time.Minute).Before(a.expiresAt) {
		t := a.token
		a.mu.RUnlock()
		return t, nil
	}
	a.mu.RUnlock()

	if a.fetchToken == nil {
		return "", nil // mock path — no real token needed
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Now().Add(5*time.Minute).Before(a.expiresAt) {
		return a.token, nil
	}
	token, ttl, err := a.fetchToken()
	if err != nil {
		return "", err
	}
	a.token = token
	a.expiresAt = time.Now().Add(ttl)
	return a.token, nil
}
