package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const maxAPIBody = 1 << 20 // 1 MiB

type vncProxyResp struct {
	Data struct {
		Ticket string      `json:"ticket"`
		Port  json.Number `json:"port"`
	} `json:"data"`
}

type vncSession struct {
	Ticket string
	Port   string
}

var (
	pveHTTPOnce sync.Once
	pveHTTP     *http.Client
)

func getPVEHTTP(insecure bool) *http.Client {
	pveHTTPOnce.Do(func() {
		pveHTTP = &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure},
			},
		}
	})
	return pveHTTP
}

func requestVNCProxy(host, tokenID, secret, node string, vmid int, insecure bool) (*vncSession, error) {
	form := url.Values{}
	form.Set("websocket", "1")
	endpoint := fmt.Sprintf("%s/api2/json/nodes/%s/qemu/%d/vncproxy",
		host, url.PathEscape(node), vmid)

	req, err := http.NewRequest("POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+tokenID+"="+secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := getPVEHTTP(insecure).Do(req)
	if err != nil {
		return nil, fmt.Errorf("vncproxy request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBody))
	if err != nil {
		return nil, fmt.Errorf("vncproxy read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("vncproxy http %d", resp.StatusCode)
	}

	var parsed vncProxyResp
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("vncproxy decode: %w", err)
	}
	if parsed.Data.Ticket == "" {
		return nil, fmt.Errorf("vncproxy empty ticket")
	}
	port := parsed.Data.Port.String()
	if port == "" {
		return nil, fmt.Errorf("vncproxy missing port")
	}
	return &vncSession{Ticket: parsed.Data.Ticket, Port: port}, nil
}
