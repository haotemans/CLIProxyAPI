package configaccess

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Issued distribution keys authenticate alongside master api-keys; unknown
// values still fail.
func TestRegisterIncludesDistributionKeys(t *testing.T) {
	t.Cleanup(func() { sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey) })
	Register(&sdkconfig.SDKConfig{
		APIKeys: []string{"sk-master"},
		DistributionKeys: []config.DistributionKey{
			{Key: "dk-issued-one"},
			{Key: "dk-issued-two"},
		},
	})
	providers := sdkaccess.RegisteredProviders()
	if len(providers) != 1 {
		t.Fatalf("registered providers = %d, want 1", len(providers))
	}
	provider := providers[0]
	for _, key := range []string{"sk-master", "dk-issued-one", "dk-issued-two"} {
		req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		result, authErr := provider.Authenticate(context.Background(), req)
		if authErr != nil || result == nil || result.Principal != key {
			t.Fatalf("key %s must authenticate: result=%+v err=%+v", key, result, authErr)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer dk-unknown")
	if _, authErr := provider.Authenticate(context.Background(), req); authErr == nil {
		t.Fatal("unknown key must not authenticate")
	}
}
