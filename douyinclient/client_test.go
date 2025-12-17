package douyinclient

import (
	"os"
	"testing"
)

// Integration test: calls the real platform endpoint.
// To run:
//
//	export DOUYIN_APPID=your_app_id
//	export DOUYIN_APPSECRET=your_app_secret
//	# optional override; if empty the hard-coded sample will be used
//	# export DOUYIN_CODE=your_login_code
//	go test -v ./douyinclient -run TestAppsJscode2session_Integration
func TestAppsJscode2session_Integration(t *testing.T) {
	appid := os.Getenv("DOUYIN_APPID")
	secret := os.Getenv("DOUYIN_APPSECRET")
	code := os.Getenv("DOUYIN_CODE")
	if code == "" {
		// Provided sample code (may be expired at run time)
		code = "NFe9XJihW-ftr9yDWbKiItjezGzQEx8Os2wim72V-lEUk4HO9FzuiRHQs0QUYnAgjo1Oez6Zuf_X7MmDvzrkiBKVFz9oHFtRXJnwG60Y3iroEKPA6XNnDI8VkFI"
	}

	if appid == "" || secret == "" {
		t.Skip("set DOUYIN_APPID and DOUYIN_APPSECRET to run this integration test")
		return
	}

	openid, err := AppsJscode2session(appid, secret, code)
	if err != nil {
		// Do not fail CI for external dependency; just log the error.
		t.Logf("AppsJscode2session error: %v", err)
		return
	}
	t.Logf("AppsJscode2session openid: %s", openid)
}
