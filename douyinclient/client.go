package douyinclient

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

const apiURL = "https://gt.igamecreator.com/gtplatform/api/apps/jscode2session"

type Response struct {
	Code    int     `json:"code"`
	Msg     string  `json:"msg"`
	Openid  *string `json:"openid,omitempty"`
	Unionid *string `json:"unionid,omitempty"`
	ErrCode int     `json:"errcode,omitempty"`
	ErrMsg  string  `json:"errmsg,omitempty"`
	Data    struct {
		Openid  *string `json:"openid,omitempty"`
		Unionid *string `json:"unionid,omitempty"`
	} `json:"data,omitempty"`
}

// AppsJscode2session calls the platform API directly and returns openid.
// Includes simple retry on transient network or 5xx HTTP errors.
func AppsJscode2session(appid, secret, code string) (string, error) {
	q := url.Values{}
	q.Set("appid", appid)
	q.Set("secret", secret)
	q.Set("code", code)

	client := &http.Client{Timeout: 8 * time.Second}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		fullURL := apiURL + "?" + q.Encode()
		req, err := http.NewRequest(http.MethodGet, fullURL, nil)
		if err != nil {
			return "", err
		}
		// Log the exact request URL being sent
		slog.Info("douyinclient request", "url", fullURL)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(200*(attempt+1)) * time.Millisecond)
			continue
		}
		// Read body once for logging and parsing
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 500 {
			lastErr = errors.New("server error: " + resp.Status)
			time.Sleep(time.Duration(200*(attempt+1)) * time.Millisecond)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			slog.Info("douyinclient non-200 response", "status", resp.StatusCode, "body", string(bodyBytes))
			// try to parse body for message
			var r Response
			_ = json.Unmarshal(bodyBytes, &r)
			if r.Msg != "" {
				return "", errors.New(r.Msg)
			}
			if r.ErrMsg != "" {
				return "", errors.New(r.ErrMsg)
			}
			return "", errors.New("http status: " + resp.Status)
		}

		var r Response
		if err := json.Unmarshal(bodyBytes, &r); err != nil {
			lastErr = err
			time.Sleep(time.Duration(200*(attempt+1)) * time.Millisecond)
			continue
		}

		slog.Info("douyinclient response", "status", resp.StatusCode, "resp", r, "body", string(bodyBytes))

		var openid string
		if r.Openid != nil {
			openid = *r.Openid
		} else if r.Data.Openid != nil {
			openid = *r.Data.Openid
		}
		if openid == "" {
			if r.Msg != "" {
				return "", errors.New(r.Msg)
			}
			if r.ErrMsg != "" {
				return "", errors.New(r.ErrMsg)
			}
			lastErr = errors.New("openid empty")
			time.Sleep(time.Duration(200*(attempt+1)) * time.Millisecond)
			continue
		}
		return openid, nil
	}

	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("request failed")
}
