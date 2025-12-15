package douyinclient

import (
    "bytes"
    "encoding/json"
    "errors"
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
func AppsJscode2session(appid, secret, code string) (string, error) {
    // Prefer application/x-www-form-urlencoded to mimic typical SDK behavior
    form := url.Values{}
    form.Set("appid", appid)
    form.Set("secret", secret)
    form.Set("code", code)

    req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewBufferString(form.Encode()))
    if err != nil {
        return "", err
    }
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

    client := &http.Client{Timeout: 8 * time.Second}
    resp, err := client.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()

    var r Response
    if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
        return "", err
    }

    // Normalize openid location
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
        return "", errors.New("openid empty")
    }

    return openid, nil
}
