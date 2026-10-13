package e3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/yorukot/nycu/internal/toolerr"
)

var pluginfilePathRE = regexp.MustCompile(`/pluginfile\.php(?:/|$)`)

// File is a downloaded Moodle file.
type File struct {
	Name     string
	MIMEType string
	Body     []byte
	// URI is the requested URL without the token, safe to show.
	URI string
}

// FileInfo is the part of a download reported as structured data.
type FileInfo struct {
	Name     string `json:"name"`
	Size     int    `json:"size"`
	MIMEType string `json:"mime_type"`
	URL      string `json:"url"`
}

// Info summarises the file without its body.
func (f File) Info() FileInfo {
	return FileInfo{Name: f.Name, Size: len(f.Body), MIMEType: f.MIMEType, URL: f.URI}
}

var (
	errOrigin      = errors.New("download URL must use the configured Moodle HTTPS origin")
	errPluginfile  = errors.New("download URL must be a Moodle pluginfile.php URL")
	errTooLarge    = errors.New("file too large")
	invalidURLHint = "Use an HTTPS pluginfile.php URL from e3p_get_content, e3p_list_announcements or e3p_get_discussion on the configured Moodle site."
)

// validateDownloadURL accepts only pluginfile.php URLs on the configured
// Moodle origin, so the token is never sent anywhere else.
func validateDownloadURL(candidate *url.URL, base string) error {
	site, err := url.Parse(base)
	if err != nil {
		return errOrigin
	}
	if candidate.Scheme != "https" || candidate.User != nil ||
		candidate.Hostname() != site.Hostname() || portOrDefault(candidate) != portOrDefault(site) {
		return errOrigin
	}
	if !pluginfilePathRE.MatchString(candidate.Path) {
		return errPluginfile
	}
	return nil
}

func portOrDefault(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	return "443"
}

// Download fetches a Moodle pluginfile URL with the token added. Errors never
// contain the URL, because the request URL carries the token.
func (c *Client) Download(ctx context.Context, rawURL string) (File, error) {
	target, err := url.Parse(rawURL)
	if err != nil || validateDownloadURL(target, c.base) != nil {
		return File{}, toolerr.New("invalid download URL", "", invalidURLHint)
	}

	query := target.Query()
	query.Del("token")
	target.Fragment = ""
	target.RawFragment = ""
	target.RawQuery = query.Encode()
	shown := target.String()

	query.Set("token", c.token)
	target.RawQuery = query.Encode()

	httpClient := *c.http
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return validateDownloadURL(req.URL, c.base)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return File{}, toolerr.New("invalid download URL", "", invalidURLHint)
	}
	req.Header.Set("User-Agent", userAgent)

	failed := func(why string) error {
		return toolerr.New("download failed", why, "Check connectivity, the configured Moodle site and file access permissions.")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, errOrigin) || errors.Is(err, errPluginfile) {
			return File{}, failed("the server redirected to a URL outside the configured Moodle site")
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return File{}, failed("request timed out")
		}
		return File{}, failed("network failure or unsupported redirect")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return File{}, failed(fmt.Sprintf("HTTP %d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxDownloadBytes+1))
	if err != nil {
		return File{}, failed("connection dropped while reading the file")
	}
	if int64(len(body)) > c.maxDownloadBytes {
		return File{}, toolerr.New(errTooLarge.Error(), fmt.Sprintf("the file is larger than %d bytes", c.maxDownloadBytes),
			"open the file on E3 directly, or raise E3_DOWNLOAD_MAX_BYTES on the server")
	}

	name := path.Base(target.Path)
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		name = params["filename"]
	}
	return File{Name: name, MIMEType: mimeType(resp.Header.Get("Content-Type"), name, body), Body: body, URI: shown}, nil
}

// mimeType prefers the server's type unless it is the generic fallback, then
// the file extension, then content sniffing.
func mimeType(header, name string, body []byte) string {
	if mediaType, _, err := mime.ParseMediaType(header); err == nil && mediaType != "application/octet-stream" && mediaType != "application/x-forcedownload" {
		return mediaType
	}
	if ext := path.Ext(name); ext != "" {
		if byExt, _, err := mime.ParseMediaType(mime.TypeByExtension(strings.ToLower(ext))); err == nil {
			return byExt
		}
	}
	mediaType, _, _ := mime.ParseMediaType(http.DetectContentType(body))
	return mediaType
}
