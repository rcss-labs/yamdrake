package awstest

// Pushing images to Floci's ECR registry without Docker. The registry is the
// plain Docker registry HTTP API (Floci serves it on its own port, as
// <endpoint>/v2/<account>/<region>/<repository>), so an image can be written
// with a few HTTP calls. The demo needs this: its seeding runs in a container
// with no Docker daemon, and Floci there is published on no host port, so a
// docker push from the host could not reach it.
//
// The image is one layer holding this program's own executable and a marker
// file, started as `<executable> idle` (sleep) or `<executable> crash <code>`
// (exit at once). The program must be a static executable for the platform
// the containers run on, and must understand those two arguments (the demo
// helper does).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	mediaManifest = "application/vnd.docker.distribution.manifest.v2+json"
	mediaConfig   = "application/vnd.docker.container.image.v1+json"
	mediaLayer    = "application/vnd.docker.image.rootfs.diff.tar.gzip"
	// binaryPath is where the executable sits inside the image.
	binaryPath = "/yumdrake-demo"
)

// UseBinaryImages makes every image this world pushes come from the running
// executable over the registry API instead of from docker build and push.
func (w *World) UseBinaryImages() { w.binaryImages = true }

func digestOfBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type blob struct {
	digest string
	data   []byte
}

// binaryImage builds the layer and config of the image for marker. exitCode
// empty means an idle container.
func binaryImage(marker, exitCode string) (layer, config blob, err error) {
	self, err := os.Executable()
	if err != nil {
		return blob{}, blob{}, fmt.Errorf("find this program's executable: %w", err)
	}
	binary, err := os.ReadFile(self) //nolint:gosec // our own executable
	if err != nil {
		return blob{}, blob{}, fmt.Errorf("read %s: %w", self, err)
	}
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	files := []struct {
		name string
		mode int64
		body []byte
	}{{strings.TrimPrefix(binaryPath, "/"), 0o755, binary}, {"marker", 0o644, []byte(marker + "\n")}}
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body)), ModTime: time.Unix(0, 0)}); err != nil {
			return blob{}, blob{}, err
		}
		if _, err := tw.Write(f.body); err != nil {
			return blob{}, blob{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return blob{}, blob{}, err
	}
	diffID := digestOfBytes(tarBuf.Bytes())
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(tarBuf.Bytes()); err != nil {
		return blob{}, blob{}, err
	}
	if err := zw.Close(); err != nil {
		return blob{}, blob{}, err
	}
	layer = blob{digest: digestOfBytes(gz.Bytes()), data: gz.Bytes()}

	command := []string{binaryPath, "idle"}
	if exitCode != "" {
		command = []string{binaryPath, "crash", exitCode}
	}
	cfg, err := json.Marshal(map[string]any{
		"architecture": runtime.GOARCH,
		"os":           "linux",
		"config":       map[string]any{"Entrypoint": command},
		"rootfs":       map[string]any{"type": "layers", "diff_ids": []string{diffID}},
	})
	if err != nil {
		return blob{}, blob{}, err
	}
	return layer, blob{digest: digestOfBytes(cfg), data: cfg}, nil
}

// pushBinaryImage writes the image for marker under repository:tag.
func (w *World) pushBinaryImage(account, region, repo, tag, marker, exitCode string) {
	w.t.Helper()
	layer, config, err := binaryImage(marker, exitCode)
	if err != nil {
		w.t.Fatalf("awstest: build the image: %v", err)
	}
	name := fmt.Sprintf("%s/%s/%s", account, region, repo)
	for _, b := range []blob{layer, config} {
		if err := w.putBlob(name, b); err != nil {
			w.t.Fatalf("awstest: push %s: %v", name, err)
		}
	}
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     mediaManifest,
		"config":        map[string]any{"mediaType": mediaConfig, "size": len(config.data), "digest": config.digest},
		"layers":        []any{map[string]any{"mediaType": mediaLayer, "size": len(layer.data), "digest": layer.digest}},
	})
	if err != nil {
		w.t.Fatalf("awstest: %v", err)
	}
	if err := w.registryDo(http.MethodPut, w.Endpoint+"/v2/"+name+"/manifests/"+tag, mediaManifest, manifest, http.StatusCreated, nil); err != nil {
		w.t.Fatalf("awstest: push %s:%s: %v", name, tag, err)
	}
}

func (w *World) putBlob(name string, b blob) error {
	if err := w.registryDo(http.MethodHead, w.Endpoint+"/v2/"+name+"/blobs/"+b.digest, "", nil, http.StatusOK, nil); err == nil {
		return nil
	}
	var location string
	if err := w.registryDo(http.MethodPost, w.Endpoint+"/v2/"+name+"/blobs/uploads/", "", nil, http.StatusAccepted, &location); err != nil {
		return err
	}
	if strings.HasPrefix(location, "/") {
		location = w.Endpoint + location
	}
	sep := "?"
	if strings.Contains(location, "?") {
		sep = "&"
	}
	return w.registryDo(http.MethodPut, location+sep+"digest="+b.digest, "application/octet-stream", b.data, http.StatusCreated, nil)
}

// registryDo sends one request and requires status want. The Location header, when asked for, is returned.
func (w *World) registryDo(method, url, contentType string, body []byte, want int, location *string) error {
	req, err := http.NewRequestWithContext(context.Background(), method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// Floci hands a /v2/ request to its registry only when the Host is localhost; with the host name of its
	// Compose service (floci:4566) the same path is taken for an S3 request and refused.
	req.Host = "localhost"
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: status %d, want %d: %s", method, url, resp.StatusCode, want, detail)
	}
	if location != nil {
		*location = resp.Header.Get("Location")
	}
	return nil
}
