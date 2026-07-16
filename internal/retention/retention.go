// SPDX-License-Identifier: Apache-2.0

// Package retention enforces a per-channel cap on relay-managed echoes: keep at
// most N echoes carrying the sync's source tag, deleting the oldest beyond it.
//
// Safety is paramount — the Ech0 delete API is admin-gated but NOT ownership
// scoped, so an admin token can delete any echo on the instance. This package
// therefore NEVER deletes without first scoping to the configured tag, and
// bounds every run by MaxDeletePerRun. If the tag cannot be resolved (nothing
// has been posted with it yet), it deletes nothing.
package retention

import (
	"context"
	"fmt"

	"github.com/lin-snow/Ech0-Relay/internal/ech0"
)

// EchoAPI is the subset of the Ech0 client retention needs (an interface so it
// can be faked in tests).
type EchoAPI interface {
	ListTags(ctx context.Context) ([]ech0.Tag, error)
	QueryEchos(ctx context.Context, tagIDs []string, sortBy, sortOrder string, page, pageSize int) (int64, []ech0.EchoItem, error)
	DeleteEcho(ctx context.Context, id string) error
}

// Config is the retention policy for one sync.
type Config struct {
	Tag             string // source tag scoping deletions (required, must be non-empty)
	Keep            int    // keep at most this many tagged echoes
	MaxDeletePerRun int    // hard cap on deletions per run (blast-radius guardrail)
}

// Summary reports what a retention pass did.
type Summary struct {
	Tag     string
	Total   int64 // tagged echoes before this pass
	Deleted int   // echoes deleted this pass
	DryRun  bool
}

// Apply enforces the retention policy. It is a no-op when Keep <= 0.
func Apply(ctx context.Context, api EchoAPI, cfg Config, dryRun bool) (Summary, error) {
	sum := Summary{Tag: cfg.Tag, DryRun: dryRun}
	if cfg.Keep <= 0 {
		return sum, nil
	}
	// Guardrail: refuse to run without a tag — deleting untagged/globally would
	// risk the user's hand-written content.
	if cfg.Tag == "" {
		return sum, fmt.Errorf("retention: refusing to run without a tag (would risk non-relay content)")
	}
	maxDelete := cfg.MaxDeletePerRun
	if maxDelete <= 0 {
		maxDelete = 50
	}

	tagID, err := resolveTagID(ctx, api, cfg.Tag)
	if err != nil {
		return sum, err
	}
	if tagID == "" {
		// Tag not created yet => nothing posted with it => nothing to prune.
		return sum, nil
	}

	total, _, err := api.QueryEchos(ctx, []string{tagID}, "created_at", "asc", 1, 1)
	if err != nil {
		return sum, fmt.Errorf("retention: count tagged echoes: %w", err)
	}
	sum.Total = total

	excess := total - int64(cfg.Keep)
	if excess <= 0 {
		return sum, nil
	}
	toDelete := int(excess)
	if toDelete > maxDelete {
		toDelete = maxDelete
	}

	_, items, err := api.QueryEchos(ctx, []string{tagID}, "created_at", "asc", 1, toDelete)
	if err != nil {
		return sum, fmt.Errorf("retention: fetch oldest tagged echoes: %w", err)
	}

	for _, item := range items {
		if sum.Deleted >= toDelete {
			break // never exceed the per-run cap even if the API returns more
		}
		if dryRun {
			sum.Deleted++
			continue
		}
		if err := api.DeleteEcho(ctx, item.ID); err != nil {
			// Report progress made so far alongside the error.
			return sum, fmt.Errorf("retention: delete echo %s: %w", item.ID, err)
		}
		sum.Deleted++
	}
	return sum, nil
}

// resolveTagID returns the id of the tag with the given name, or "" if absent.
func resolveTagID(ctx context.Context, api EchoAPI, name string) (string, error) {
	tags, err := api.ListTags(ctx)
	if err != nil {
		return "", fmt.Errorf("retention: list tags: %w", err)
	}
	for _, tg := range tags {
		if tg.Name == name {
			return tg.ID, nil
		}
	}
	return "", nil
}
