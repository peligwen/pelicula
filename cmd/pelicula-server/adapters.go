package main

import (
	"context"

	"pelicula/internal/api"
	"pelicula/internal/auth"
	"pelicula/internal/autowire"
	"pelicula/internal/clients/gluetun"
	"pelicula/internal/clients/jellyfin"
	"pelicula/internal/clients/qbt"
)

// The handler packages declare narrow interfaces with their own value types
// so they can be tested without the real clients. Go cannot treat two
// identical structs from different packages as one, so these adapters do
// the field-for-field copy at the one place the concrete clients are known.

// identity adapts the Jellyfin login call to auth.Identity.
func identity(jf *jellyfin.Client) auth.Identity {
	return auth.IdentityFunc(func(ctx context.Context, username, password string) (*auth.LoginResult, error) {
		r, err := jf.AuthenticateByName(ctx, username, password)
		if err != nil {
			return nil, err
		}
		return &auth.LoginResult{Token: r.Token, UserID: r.UserID, Username: r.Username, IsAdmin: r.IsAdmin}, nil
	})
}

// jfUsers satisfies api.JellyfinClient.
type jfUsers struct{ *jellyfin.Client }

func (j jfUsers) ListUsers(ctx context.Context, token string) ([]api.JellyfinUser, error) {
	users, err := j.Client.ListUsers(ctx, token)
	if err != nil {
		return nil, err
	}
	out := make([]api.JellyfinUser, 0, len(users))
	for _, u := range users {
		out = append(out, api.JellyfinUser{ID: u.ID, Name: u.Name, IsAdmin: u.IsAdmin, IsDisabled: u.IsDisabled, LastLogin: u.LastLogin})
	}
	return out, nil
}

// jfLibraries satisfies autowire.JellyfinClient.
type jfLibraries struct{ *jellyfin.Client }

func (j jfLibraries) ListLibraries(ctx context.Context, token string) ([]autowire.Library, error) {
	libs, err := j.Client.ListLibraries(ctx, token)
	if err != nil {
		return nil, err
	}
	out := make([]autowire.Library, 0, len(libs))
	for _, l := range libs {
		out = append(out, autowire.Library{Name: l.Name, CollectionType: l.CollectionType, Locations: l.Locations})
	}
	return out, nil
}

// qbtAdapter satisfies api.QBTClient.
type qbtAdapter struct{ *qbt.Client }

func (q qbtAdapter) ListTorrents(ctx context.Context) ([]api.Torrent, error) {
	ts, err := q.Client.ListTorrents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]api.Torrent, 0, len(ts))
	for _, t := range ts {
		out = append(out, api.Torrent{
			Hash: t.Hash, Name: t.Name, State: t.State, Category: t.Category,
			Progress: t.Progress, Dlspeed: t.Dlspeed, Upspeed: t.Upspeed, Eta: t.Eta, Size: t.Size,
		})
	}
	return out, nil
}

func (q qbtAdapter) GetTransferInfo(ctx context.Context) (*api.TransferInfo, error) {
	ti, err := q.Client.GetTransferInfo(ctx)
	if err != nil {
		return nil, err
	}
	return &api.TransferInfo{DlSpeed: ti.DlSpeed, UpSpeed: ti.UpSpeed}, nil
}

// gluetunAdapter satisfies api.GluetunClient.
type gluetunAdapter struct{ *gluetun.Client }

func (g gluetunAdapter) GetPublicIP(ctx context.Context) (*api.VPNStatus, error) {
	s, err := g.Client.GetPublicIP(ctx)
	if err != nil {
		return nil, err
	}
	return &api.VPNStatus{PublicIP: s.PublicIP, Country: s.Country, City: s.City}, nil
}
