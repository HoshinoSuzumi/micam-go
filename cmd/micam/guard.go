package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"micam/internal/auth"
	"micam/internal/cli"
	"micam/internal/creds"
)

// buildAuth turns the flags into the policy the stream server enforces.
//
// An empty token list means no access control: that is the historical behaviour,
// and changing it silently would break every URL already saved in a player.
func buildAuth(opts *cli.Options, log *slog.Logger) (*auth.Policy, error) {
	tokens := make([]auth.Token, 0, len(opts.Tokens))
	for i, raw := range opts.Tokens {
		label, secret, hasLabel := strings.Cut(raw, "=")
		if !hasLabel {
			label, secret = "", raw
		}
		label, secret = strings.TrimSpace(label), strings.TrimSpace(secret)
		if secret == "" {
			return nil, fmt.Errorf("token %d has an empty secret", i+1)
		}
		if label == "" {
			label = fmt.Sprintf("token-%d", i+1)
		}
		tokens = append(tokens, auth.Token{Label: label, Secret: secret})
	}

	build := func(key []byte) (*auth.Policy, error) {
		return auth.New(auth.Options{
			Realm:          opts.AuthRealm,
			Tokens:         tokens,
			CIDRs:          opts.AllowCIDRs,
			Ifaces:         opts.TrustIfaces,
			TrustedProxies: opts.TrustedProxies,
			NoLoopback:     opts.NoLocalBypass,
			FailLimit:      opts.AuthFailLimit,
			ShareKey:       key,
			ShareTTL:       opts.ShareTTL,
		})
	}

	if len(tokens) == 0 {
		// With no credential required there is nothing for a link to prove, so
		// no signing key is created either.
		return build(nil)
	}

	key, err := shareKey(opts)
	if err != nil {
		// A key the operator typed is a typo and worth stopping for; a key that
		// could not be created on disk is not, and must not take the stream down
		// with it. Links are a convenience.
		if opts.ShareKey != "" {
			return nil, err
		}
		log.Warn("expiring links are unavailable: cannot prepare the signing key", "err", err)
		return build(nil)
	}
	return build(key)
}

// shareKey is the HMAC key behind expiring links.
func shareKey(opts *cli.Options) ([]byte, error) {
	if opts.ShareKey != "" {
		key, err := hex.DecodeString(strings.TrimSpace(opts.ShareKey))
		if err != nil || len(key) != 32 {
			return nil, errors.New("share-key must be 64 hex characters")
		}
		return key, nil
	}
	store, err := creds.StoreAt(opts.DataDir)
	if err != nil {
		return nil, err
	}
	return auth.LoadOrCreateShareKey(auth.ShareKeyPath(filepath.Dir(store.Path)))
}
