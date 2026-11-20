package ctl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"micam/internal/cloud"
	"micam/internal/creds"
)

// Camera is one device on the account, as the picker needs it.
type Camera struct {
	DID    string `json:"did"`
	Name   string `json:"name"`
	Model  string `json:"model"`
	Online bool   `json:"online"`
}

// cameras lists what the account owns, so the UI can offer a picker instead of
// asking for a numeric DID that only the command line could tell you.
func (s *Server) cameras() reply {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := creds.StoreAt(s.Paths.DataDir())
	if err != nil {
		return s.fail(http.StatusInternalServerError, err)
	}
	creds, err := store.Load()
	if err != nil {
		return s.fail(http.StatusInternalServerError, fmt.Errorf("cannot read the cached session: %w", err))
	}
	if creds.Region == "" {
		creds.Region = "cn"
	}
	// The stored session usually has what it needs; the token file is the fallback
	// for a fresh install that has not started once yet.
	if token := s.Paths.ReadToken(); token != "" {
		creds.PassToken = token
	}

	client, err := cloud.New(creds)
	if err != nil {
		return s.fail(http.StatusInternalServerError, err)
	}

	devices, err := s.listCameras(ctx, client, store, creds)
	if err != nil {
		return s.fail(http.StatusBadGateway, err)
	}

	out := make([]Camera, 0, len(devices))
	for _, d := range devices {
		if !d.IsCamera() {
			continue
		}
		out = append(out, Camera{DID: d.DID, Name: d.Name, Model: d.Model, Online: d.Online})
	}
	return s.json(http.StatusOK, map[string]any{"ok": true, "cameras": out})
}

// listCameras reads the device list, renewing the session once if it expired.
func (s *Server) listCameras(ctx context.Context, client *cloud.Client,
	store *creds.Store, c *creds.Credentials) ([]cloud.Device, error) {

	if !c.HasSession() {
		if err := client.Login(ctx, ""); err != nil {
			return nil, err
		}
		_ = store.Save(c)
	}

	devices, err := client.DeviceList(ctx)
	if err == nil {
		return devices, nil
	}

	// Most likely an expired serviceToken: drop it and log in again, the same way
	// the daemon does at startup.
	c.Ssecurity, c.ServiceToken = "", ""
	if loginErr := client.Login(ctx, ""); loginErr != nil {
		return nil, errors.Join(err, loginErr)
	}
	if saveErr := store.Save(c); saveErr != nil && s.Log != nil {
		s.Log.Warn("cannot save the renewed session", "err", saveErr)
	}

	return client.DeviceList(ctx)
}
