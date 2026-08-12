// Package config loads the lifecycle-manager configuration from HLM_*
// environment variables. Secrets support *_FILE variants; when both are set
// the file wins (matching the connector's convention).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const inClusterNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// Config is the fully-resolved runtime configuration.
type Config struct {
	Listen    string
	Namespace string
	// WarmPool is the default SandboxWarmPool new sessions claim from
	// (v1beta1 claims reference pools, not templates; see docs/agent-sandbox.md).
	WarmPool string

	SandboxAPIGroup    string // group of Sandbox (agents.x-k8s.io)
	SandboxExtAPIGroup string // group of SandboxClaim/Template/WarmPool
	SandboxAPIVersion  string

	SweepInterval time.Duration
	IdleTimeout   time.Duration // 0 disables the idle sweeper
	TTL           time.Duration // 0 disables the TTL sweeper

	APIToken string // optional bearer for /v1/*; never guards /wake or health

	ConnectorEnabled        bool
	ConnectorURL            string // admin-plane base URL
	ConnectorAdminToken     string
	ConnectorProvisionToken string
	ConnectorBotID          string
	ConnectorPlatform       string
	WakeBaseURL             string
	OrphanPolicy            string // "report" (only value implemented)

	// Idle v2 (issue #2): poll session pods' /api/status so suspends key
	// off agent-reported activity, not just relay traffic.
	StatusEnabled  bool          // opt-in: sessions must run the serve container
	StatusPort     int           // hermes serve port on session pods
	StatusTimeout  time.Duration // per-poll HTTP timeout
	StatusUsername string        // basic-auth credential for the gated cron endpoint
	StatusPassword string        // (both empty = cron guard skipped)
	IdleHorizon    time.Duration // cron fire within this window blocks a suspend
	WakeBootMargin time.Duration // scheduled wakes fire this early before the cron

	// Exposure (PLAN T4): per-session Services (Idle v2 poll targets —
	// agent-sandbox creates none, issue #11) and dashboard Ingresses.
	ExposureEnabled       bool
	DashboardDomain       string // "" = Services only, no Ingresses
	DashboardIngressClass string
	DashboardTLSSecret    string // pre-issued wildcard cert Secret
	DashboardDenyService  string // no-endpoints Service for the edge deny-list

	LogLevel  string
	LogFormat string
}

// Load reads and validates configuration from the environment.
func Load() (*Config, error) {
	c := &Config{
		Listen:             getenv("HLM_LISTEN", ":8080"),
		Namespace:          getenv("HLM_NAMESPACE", ""),
		WarmPool:           getenv("HLM_WARM_POOL", ""),
		SandboxAPIGroup:    getenv("HLM_SANDBOX_API_GROUP", "agents.x-k8s.io"),
		SandboxExtAPIGroup: getenv("HLM_SANDBOX_EXT_API_GROUP", "extensions.agents.x-k8s.io"),
		SandboxAPIVersion:  getenv("HLM_SANDBOX_API_VERSION", "v1beta1"),
		ConnectorURL:       strings.TrimRight(getenv("HLM_CONNECTOR_URL", ""), "/"),
		ConnectorBotID:     getenv("HLM_CONNECTOR_BOT_ID", ""),
		ConnectorPlatform:  getenv("HLM_CONNECTOR_PLATFORM", "discord"),
		WakeBaseURL:        strings.TrimRight(getenv("HLM_WAKE_BASE_URL", ""), "/"),
		OrphanPolicy:       getenv("HLM_ORPHAN_POLICY", "report"),
		LogLevel:           getenv("HLM_LOG_LEVEL", "info"),
		LogFormat:          getenv("HLM_LOG_FORMAT", "json"),
	}

	var err error
	if c.SweepInterval, err = getDuration("HLM_SWEEP_INTERVAL", time.Minute); err != nil {
		return nil, err
	}
	if c.IdleTimeout, err = getDuration("HLM_IDLE_TIMEOUT", 30*time.Minute); err != nil {
		return nil, err
	}
	if c.TTL, err = getDuration("HLM_TTL", 0); err != nil {
		return nil, err
	}
	if c.ConnectorEnabled, err = getBool("HLM_CONNECTOR_ENABLED", false); err != nil {
		return nil, err
	}
	if c.APIToken, err = loadSecret("HLM_API_TOKEN"); err != nil {
		return nil, err
	}
	if c.ConnectorAdminToken, err = loadSecret("HLM_CONNECTOR_ADMIN_TOKEN"); err != nil {
		return nil, err
	}
	if c.ConnectorProvisionToken, err = loadSecret("HLM_CONNECTOR_PROVISION_TOKEN"); err != nil {
		return nil, err
	}
	if c.StatusEnabled, err = getBool("HLM_STATUS_ENABLED", false); err != nil {
		return nil, err
	}
	if c.StatusPort, err = getInt("HLM_STATUS_PORT", 9119); err != nil {
		return nil, err
	}
	if c.StatusTimeout, err = getDuration("HLM_STATUS_TIMEOUT", 3*time.Second); err != nil {
		return nil, err
	}
	c.StatusUsername = getenv("HLM_STATUS_BASIC_AUTH_USERNAME", "")
	if c.StatusPassword, err = loadSecret("HLM_STATUS_BASIC_AUTH_PASSWORD"); err != nil {
		return nil, err
	}
	if c.IdleHorizon, err = getDuration("HLM_IDLE_HORIZON", 5*time.Minute); err != nil {
		return nil, err
	}
	if c.WakeBootMargin, err = getDuration("HLM_WAKE_BOOT_MARGIN", 2*time.Minute); err != nil {
		return nil, err
	}
	if c.ExposureEnabled, err = getBool("HLM_EXPOSURE_ENABLED", false); err != nil {
		return nil, err
	}
	c.DashboardDomain = getenv("HLM_DASHBOARD_DOMAIN", "")
	c.DashboardIngressClass = getenv("HLM_DASHBOARD_INGRESS_CLASS", "webapprouting.kubernetes.azure.com")
	c.DashboardTLSSecret = getenv("HLM_DASHBOARD_TLS_SECRET", "")
	c.DashboardDenyService = getenv("HLM_DASHBOARD_DENY_SERVICE", "")

	if c.Namespace == "" {
		if b, err := os.ReadFile(inClusterNamespaceFile); err == nil {
			c.Namespace = strings.TrimSpace(string(b))
		}
	}
	if c.Namespace == "" {
		c.Namespace = "default"
	}

	if c.WarmPool == "" {
		return nil, fmt.Errorf("HLM_WARM_POOL is required (name of the default SandboxWarmPool for new sessions)")
	}
	if c.SweepInterval < time.Second {
		return nil, fmt.Errorf("HLM_SWEEP_INTERVAL must be >= 1s, got %s", c.SweepInterval)
	}
	if c.OrphanPolicy != "report" {
		return nil, fmt.Errorf("HLM_ORPHAN_POLICY: only \"report\" is implemented, got %q", c.OrphanPolicy)
	}
	if c.ConnectorEnabled {
		if c.ConnectorURL == "" {
			return nil, fmt.Errorf("HLM_CONNECTOR_URL is required when HLM_CONNECTOR_ENABLED=true")
		}
		if c.ConnectorAdminToken == "" {
			return nil, fmt.Errorf("HLM_CONNECTOR_ADMIN_TOKEN(_FILE) is required when HLM_CONNECTOR_ENABLED=true")
		}
		if c.WakeBaseURL == "" {
			return nil, fmt.Errorf("HLM_WAKE_BASE_URL is required when HLM_CONNECTOR_ENABLED=true (the connector pokes <base>/wake/<session>)")
		}
		if c.ConnectorProvisionToken != "" && c.ConnectorBotID == "" {
			return nil, fmt.Errorf("HLM_CONNECTOR_BOT_ID is required when a provision token is configured")
		}
	}
	if c.StatusEnabled {
		if c.StatusPort < 1 || c.StatusPort > 65535 {
			return nil, fmt.Errorf("HLM_STATUS_PORT must be a valid port, got %d", c.StatusPort)
		}
		if c.StatusTimeout < time.Second {
			return nil, fmt.Errorf("HLM_STATUS_TIMEOUT must be >= 1s, got %s", c.StatusTimeout)
		}
		if (c.StatusUsername == "") != (c.StatusPassword == "") {
			return nil, fmt.Errorf("HLM_STATUS_BASIC_AUTH_USERNAME and HLM_STATUS_BASIC_AUTH_PASSWORD(_FILE) must be set together")
		}
		if c.IdleHorizon < c.WakeBootMargin {
			return nil, fmt.Errorf("HLM_IDLE_HORIZON (%s) must be >= HLM_WAKE_BOOT_MARGIN (%s): a wake scheduled inside the margin would fire immediately", c.IdleHorizon, c.WakeBootMargin)
		}
	}
	if c.StatusEnabled && !c.ExposureEnabled {
		// Not fatal for forward compatibility (a future agent-sandbox may
		// publish serviceFQDN), but on v0.5.4 status polling without the
		// exposure Services can never reach a pod — issue #11 exactly.
		fmt.Fprintln(os.Stderr, "warning: HLM_STATUS_ENABLED without HLM_EXPOSURE_ENABLED: status polls have no target on agent-sandbox v0.5.4 (issue #11) and every suspend will be deferred")
	}
	if c.DashboardDomain != "" {
		if !c.ExposureEnabled {
			return nil, fmt.Errorf("HLM_DASHBOARD_DOMAIN requires HLM_EXPOSURE_ENABLED=true (Ingresses back onto the per-session Services)")
		}
		if c.DashboardTLSSecret == "" {
			return nil, fmt.Errorf("HLM_DASHBOARD_TLS_SECRET is required when HLM_DASHBOARD_DOMAIN is set (the wildcard cert Secret)")
		}
		if c.DashboardDenyService == "" {
			return nil, fmt.Errorf("HLM_DASHBOARD_DENY_SERVICE is required when HLM_DASHBOARD_DOMAIN is set (the /auth/password-login edge block must ship WITH the first Ingress, never after)")
		}
	}
	return c, nil
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getBool(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes":
		return true, nil
	case "0", "false", "no":
		return false, nil
	}
	return false, fmt.Errorf("%s: cannot parse %q as bool", key, v)
}

func getInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: cannot parse %q as integer", key, v)
	}
	return n, nil
}

// getDuration accepts Go duration strings ("30m") or bare integers (seconds).
func getDuration(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Duration(secs) * time.Second, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: cannot parse %q as duration", key, v)
	}
	return d, nil
}

// loadSecret resolves KEY / KEY_FILE. A readable file wins over the plain
// variable; an unreadable or empty file is an error rather than a silent
// fallback (a misconfigured mount should fail loudly).
func loadSecret(key string) (string, error) {
	if path, ok := os.LookupEnv(key + "_FILE"); ok && path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", key, err)
		}
		s := strings.TrimSpace(string(b))
		if s == "" {
			return "", fmt.Errorf("%s_FILE: file %q is empty", key, path)
		}
		return s, nil
	}
	return os.Getenv(key), nil
}
