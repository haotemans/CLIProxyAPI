// Package mirasim implements the Mirasim OAuth login flow, the credential
// file format, and the relay's signed-request protocol as a native provider.
//
// Derived from KIDA-MNESIA/cpa-plugin-mirasim (MIT); the protocol constants and
// flow behavior are ported 1:1 from the community plugin so existing mirasim
// OAuth credentials keep working after the plugin is uninstalled.
package mirasim

const (
	// Provider is the credential/provider key shared with the native
	// api-keys.mirasim provider (the claude coexistence pattern).
	Provider = "mirasim"

	// DefaultRelayURL is the inference relay origin mirasim accounts use.
	DefaultRelayURL = "https://relay.mirasim.ai"
	// DefaultAdminURL is the authentication service origin used by OAuth login,
	// token refresh, and account profile reads.
	DefaultAdminURL = "https://auth.mirasim.ai"
	// DefaultClientVersion is sent in x-mirasim-client: the official client
	// protocol revision this code speaks.
	DefaultClientVersion = "0.0.372"
	// DefaultLoginProvider is the Mirasim sign-in provider used when the login
	// caller does not request one.
	DefaultLoginProvider = "github"
)

// Settings collects the public endpoints for one login. Credential material is
// supplied only by OAuth and persisted in auth-dir.
type Settings struct {
	RelayURL      string
	AdminURL      string
	ClientVersion string
	LoginProvider string
	// CallbackPort pins the loopback listener port for the CLI login
	// (-oauth-callback-port). 0 takes an ephemeral port.
	CallbackPort int
	// ProxyURL routes outbound authentication-service calls; empty/direct uses
	// a plain transport.
	ProxyURL string
}

// Normalize applies defaults for every empty field.
func (s Settings) Normalize() Settings {
	if s.RelayURL == "" {
		s.RelayURL = DefaultRelayURL
	}
	if s.AdminURL == "" {
		s.AdminURL = DefaultAdminURL
	}
	if s.ClientVersion == "" {
		s.ClientVersion = DefaultClientVersion
	}
	if s.LoginProvider == "" {
		s.LoginProvider = DefaultLoginProvider
	}
	return s
}

// SettingsFromConfig derives login settings from the global config proxy;
// provider endpoints stay at their public defaults (mirrors the plugin).
func SettingsFromConfig(proxyURL string) Settings {
	return Settings{ProxyURL: proxyURL}.Normalize()
}
