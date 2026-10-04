package mirasim

import "testing"

func TestParseModelLimitWindowsAndExhaustion(t *testing.T) {
	raw := []byte(`{"windows":[
		{"name":"weekly","budget":100,"used":60},
		{"name":"kimi-k3","budget":40,"used":40,"model_scoped":true},
		{"name":"deepseek-flash","budget":40,"used":12,"model_scoped":true},
		{"name":"glm-5.3-flash"},
		{"name":"","budget":1,"used":0}
	],"paid":false}`)
	windows := ParseModelLimitWindows(raw)
	if len(windows) != 3 {
		t.Fatalf("windows = %+v", windows)
	}
	if !ModelLimitExhausted(windows, "kimi-k3") {
		t.Fatal("kimi-k3 must read exhausted at used==budget")
	}
	if ModelLimitExhausted(windows, "deepseek-flash") {
		t.Fatal("deepseek-flash has budget left")
	}
	if ModelLimitExhausted(windows, "weekly") {
		t.Fatal("account windows never mark a model")
	}
	if ModelLimitExhausted(windows, "unknown-model") {
		t.Fatal("missing windows never trip preflight")
	}
	if !ModelLimitExhausted(windows, "KIMI-K3") {
		t.Fatal("model names compare case-insensitively")
	}
}

func TestModelLimitExhaustedJunk(t *testing.T) {
	if ModelLimitExhausted(ParseModelLimitWindows([]byte(`not json`)), "kimi-k3") {
		t.Fatal("invalid payload must not trip preflight")
	}
	if ModelLimitExhausted(ParseModelLimitWindows([]byte(`{}`)), "kimi-k3") {
		t.Fatal("empty payload must not trip preflight")
	}
}
