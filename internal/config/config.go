// SPDX-License-Identifier: Apache-2.0

// Package config loads and validates the relay configuration (config.yaml).
//
// The config is intentionally non-secret and committed to the repository:
// instance base URLs, channel->instance mappings and per-sync knobs. Access
// tokens never live here — each instance references an environment variable
// name (token_env) that the CI workflow maps from a GitHub Actions secret.
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Default values applied when a field is left at its zero value.
const (
	defaultMaxPerRun       = 10
	defaultMaxDeletePerRun = 50
	defaultBackfillLimit   = 20
)

// Instance describes a single target Ech0 instance.
type Instance struct {
	// BaseURL is the instance root, e.g. https://echo.example.com (no trailing /api).
	BaseURL string `yaml:"base_url"`
	// TokenEnv is the name of the environment variable holding the access token.
	// The token itself is never stored in the config file.
	TokenEnv string `yaml:"token_env"`
}

// Token reads the access token for this instance from the environment.
func (i Instance) Token() string { return os.Getenv(i.TokenEnv) }

// Sync describes one channel->instance relay job.
type Sync struct {
	// Name is the stable state key. Defaults to "<instance>/<channel>".
	Name string `yaml:"name"`
	// Channel is the public Telegram channel slug (t.me/s/<channel>).
	Channel string `yaml:"channel"`
	// Instance is the key into Config.Instances this sync posts to.
	Instance string `yaml:"instance"`
	// Tag is the source tag stamped on every posted echo. Required when Keep > 0
	// (retention identifies relay-managed echoes by this tag) and must be unique
	// among retention-enabled syncs targeting the same instance.
	Tag string `yaml:"tag"`
	// MaxPerRun caps how many new posts are published per run (oldest first).
	MaxPerRun int `yaml:"max_per_run"`
	// WithSourceLink appends a "via t.me/<channel>/<id>" footer to each echo.
	WithSourceLink bool `yaml:"with_source_link"`
	// Private posts the echo as private when true.
	Private bool `yaml:"private"`
	// Keep is the retention cap: keep at most Keep tagged echoes on the instance,
	// deleting the oldest beyond it. 0 (default) disables retention.
	Keep int `yaml:"keep"`
	// MaxDeletePerRun bounds deletions per run (blast-radius guardrail).
	MaxDeletePerRun int `yaml:"max_delete_per_run"`
	// BackfillOnFirstRun controls first-run behavior when no cursor exists:
	// false (default) seeds the cursor to the newest id without posting history;
	// true posts up to BackfillLimit oldest messages.
	BackfillOnFirstRun bool `yaml:"backfill_on_first_run"`
	// BackfillLimit caps history posted on first run (only used when backfill on).
	BackfillLimit int `yaml:"backfill_limit"`
}

// Config is the root document.
type Config struct {
	Instances map[string]Instance `yaml:"instances"`
	Syncs     []Sync              `yaml:"syncs"`
}

// Load reads, parses, normalizes and validates the config at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // fail loudly on typo'd keys
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// normalize applies defaults and cleans up user-provided values in place.
func (c *Config) normalize() {
	for name, inst := range c.Instances {
		inst.BaseURL = strings.TrimRight(strings.TrimSpace(inst.BaseURL), "/")
		inst.TokenEnv = strings.TrimSpace(inst.TokenEnv)
		c.Instances[name] = inst
	}
	for i := range c.Syncs {
		s := &c.Syncs[i]
		s.Channel = strings.TrimSpace(strings.TrimPrefix(s.Channel, "@"))
		s.Instance = strings.TrimSpace(s.Instance)
		s.Tag = strings.TrimSpace(s.Tag)
		if s.Name == "" {
			s.Name = s.Instance + "/" + s.Channel
		}
		if s.MaxPerRun <= 0 {
			s.MaxPerRun = defaultMaxPerRun
		}
		if s.MaxDeletePerRun <= 0 {
			s.MaxDeletePerRun = defaultMaxDeletePerRun
		}
		if s.BackfillLimit <= 0 {
			s.BackfillLimit = defaultBackfillLimit
		}
	}
}

// Validate enforces referential integrity and the retention safety invariants.
func (c *Config) Validate() error {
	if len(c.Instances) == 0 {
		return fmt.Errorf("config: no instances defined")
	}
	for name, inst := range c.Instances {
		if inst.BaseURL == "" {
			return fmt.Errorf("config: instance %q missing base_url", name)
		}
		if inst.TokenEnv == "" {
			return fmt.Errorf("config: instance %q missing token_env", name)
		}
	}
	if len(c.Syncs) == 0 {
		return fmt.Errorf("config: no syncs defined")
	}

	seenName := make(map[string]bool)
	// retentionTags[instance][tag] guards against two retention-enabled syncs on
	// the same instance sharing a tag, which would cross-count and mis-delete.
	retentionTags := make(map[string]map[string]string)

	for _, s := range c.Syncs {
		if s.Channel == "" {
			return fmt.Errorf("config: sync %q missing channel", s.Name)
		}
		if _, ok := c.Instances[s.Instance]; !ok {
			return fmt.Errorf("config: sync %q references unknown instance %q", s.Name, s.Instance)
		}
		if seenName[s.Name] {
			return fmt.Errorf("config: duplicate sync name %q", s.Name)
		}
		seenName[s.Name] = true

		if s.Keep > 0 {
			if s.Tag == "" {
				return fmt.Errorf("config: sync %q has keep=%d but no tag; retention requires a tag to safely scope deletions", s.Name, s.Keep)
			}
			byTag := retentionTags[s.Instance]
			if byTag == nil {
				byTag = make(map[string]string)
				retentionTags[s.Instance] = byTag
			}
			if other, ok := byTag[s.Tag]; ok {
				return fmt.Errorf("config: syncs %q and %q on instance %q share retention tag %q; each retention-enabled sync needs a distinct tag", other, s.Name, s.Instance, s.Tag)
			}
			byTag[s.Tag] = s.Name
		}
	}
	return nil
}
