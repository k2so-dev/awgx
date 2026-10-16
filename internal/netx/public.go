package netx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

var PublicIPServices = []string{"https://api.ipify.org", "https://ifconfig.me/ip"}

func PublicIPv4(ctx context.Context) (netip.Addr, error) {
	d := &net.Dialer{Timeout: 5 * time.Second}
	client := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp4", addr)
		}},
	}
	var errs []error
	for _, url := range PublicIPServices {
		a, err := fetchIP(ctx, client, url)
		if err == nil {
			return a, nil
		}
		errs = append(errs, err)
	}
	return netip.Addr{}, errors.Join(errs...)
}

func fetchIP(ctx context.Context, client *http.Client, url string) (netip.Addr, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	req.Header.Set("User-Agent", "curl/8")
	resp, err := client.Do(req)
	if err != nil {
		return netip.Addr{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return netip.Addr{}, err
	}
	a, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil || !a.Is4() || a.IsPrivate() || a.IsLoopback() || a.IsUnspecified() {
		return netip.Addr{}, errors.New(url + ": no public IPv4 address in the answer")
	}
	return a, nil
}
