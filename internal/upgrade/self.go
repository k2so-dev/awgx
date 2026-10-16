package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/jedisct1/go-minisign"
	"golang.org/x/mod/semver"
)

var (
	PublicKey = ""
	Repo      = "k2so-dev/awgx"
)

const maxBinary = 64 << 20

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type Release struct {
	Tag        string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	Assets     []asset `json:"assets"`
}

func (r Release) asset(name string) (string, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL, true
		}
	}
	return "", false
}

func Latest(ctx context.Context, channel string) (Release, error) {
	var releases []Release
	if err := getJSON(ctx, "https://api.github.com/repos/"+Repo+"/releases?per_page=30", &releases); err != nil {
		return Release{}, err
	}
	var best Release
	for _, r := range releases {
		if r.Draft || !semver.IsValid(r.Tag) || r.Prerelease && channel != "beta" {
			continue
		}
		if best.Tag == "" || semver.Compare(r.Tag, best.Tag) > 0 {
			best = r
		}
	}
	if best.Tag == "" {
		return best, errors.New("no releases found")
	}
	return best, nil
}

var describeSuffix = regexp.MustCompile(`(-[0-9]+-g[0-9a-f]+)?(-dirty)?$`)

func release(current string) string {
	if !strings.HasPrefix(current, "v") {
		current = "v" + current
	}
	return describeSuffix.ReplaceAllString(current, "")
}

func Newer(current, tag string) bool {
	current = release(current)
	return !semver.IsValid(current) || semver.Compare(tag, current) > 0
}

func Channel(current string) string {
	if semver.Prerelease(release(current)) != "" {
		return "beta"
	}
	return "stable"
}

func CheckSelf(ctx context.Context, current string) (string, error) {
	r, err := Latest(ctx, Channel(current))
	if err != nil {
		return "", err
	}
	if Newer(current, r.Tag) {
		return r.Tag, nil
	}
	return "", nil
}

func Self(ctx context.Context, binary, channel, current string) (string, error) {
	if PublicKey == "" {
		return "", errors.New("self-update is disabled: this build has no release signing key")
	}
	pk, err := minisign.NewPublicKey(PublicKey)
	if err != nil {
		return "", fmt.Errorf("release key: %w", err)
	}
	if channel == "" {
		channel = Channel(current)
	}
	r, err := Latest(ctx, channel)
	if err != nil {
		return "", err
	}
	if !Newer(current, r.Tag) {
		return "", nil
	}
	name := "awgx-linux-" + runtime.GOARCH
	binURL, ok := r.asset(name)
	sigURL, ok2 := r.asset(name + ".minisig")
	if !ok || !ok2 {
		return "", fmt.Errorf("release %s has no %s with a signature", r.Tag, name)
	}
	bin, err := get(ctx, binURL, maxBinary)
	if err != nil {
		return "", err
	}
	sigText, err := get(ctx, sigURL, 4096)
	if err != nil {
		return "", err
	}
	sig, err := minisign.DecodeSignature(string(sigText))
	if err != nil {
		return "", fmt.Errorf("signature: %w", err)
	}
	if ok, err := pk.Verify(bin, sig); !ok {
		return "", fmt.Errorf("signature verification failed: %v", err)
	}
	if want := "trusted comment: " + TrustedComment(r.Tag, name); sig.TrustedComment != want {
		return "", fmt.Errorf("signature is for %q, expected %q", sig.TrustedComment, want)
	}
	tmp := binary + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, binary); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return r.Tag, nil
}

func getJSON(ctx context.Context, url string, v any) error {
	b, err := get(ctx, url, 4<<20)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func get(ctx context.Context, url string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("GET %s: response too large", url)
	}
	return b, nil
}

func TrustedComment(tag, asset string) string {
	return "awgx " + tag + " " + asset
}
