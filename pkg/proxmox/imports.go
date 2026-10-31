package proxmox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/luthermonson/go-proxmox"
)

// Files that populate a volume reach the storage through the Proxmox upload
// API, never through download-url: the operator fetches a URL itself, so
// the hypervisor talks to nothing a tenant names, and no allow-list of hosts
// is needed on it.

const (
	// ContentImport is the storage content type of an image to import-from.
	ContentImport = "import"
	// ContentISO is the storage content type of a CD-ROM image.
	ContentISO = "iso"

	// importDownloadTimeout bounds one download; the upload task that follows
	// gets diskImportTimeout.
	importDownloadTimeout = 2 * time.Hour
)

// ImportFilename is the name a file populating a volume gets on the storage:
// after the volume, so a leftover is traceable, with the extension of the
// source, which is how Proxmox tells the image format.
func ImportFilename(namespace, name, source string) string {
	base := fmt.Sprintf("vmvol-%s-%s", namespace, name)
	if u, err := url.Parse(source); err == nil && u.Path != "" {
		source = u.Path
	}
	ext := strings.ToLower(path.Ext(source))
	if ext == "" {
		// shortcut: an extensionless URL is taken as a raw image, since Proxmox
		// needs some extension; add content sniffing when a tenant hits this.
		ext = ".raw"
	}
	return base + ext
}

// StorageFileVolid is the volume id of a file on a storage.
func StorageFileVolid(storage, content, filename string) string {
	return fmt.Sprintf("%s:%s/%s", storage, content, filename)
}

// UploadFromURL downloads source and uploads it, under filename and the given
// content type, to the storage of node. It blocks for the whole transfer and
// returns the volume id of the file. A body of unknown length is spooled to a
// temporary file first, because the upload API wants Content-Length.
func (pc *ProxmoxClient) UploadFromURL(ctx context.Context, node, storage, content, filename, source string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, importDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", source, err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", source, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: HTTP %s", source, res.Status)
	}
	var body io.Reader = res.Body
	size := res.ContentLength
	if size < 0 {
		tmp, err := os.CreateTemp("", "vmvol-*")
		if err != nil {
			return "", fmt.Errorf("spooling %s: %w", source, err)
		}
		defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
		if size, err = io.Copy(tmp, res.Body); err != nil {
			return "", fmt.Errorf("downloading %s: %w", source, err)
		}
		if _, err = tmp.Seek(0, io.SeekStart); err != nil {
			return "", fmt.Errorf("spooling %s: %w", source, err)
		}
		body = tmp
	}
	return pc.UploadFile(ctx, node, storage, content, filename, body, size)
}

// UploadFile streams size bytes of body to the storage of node as a file of
// the content type and waits for Proxmox to move it into place.
func (pc *ProxmoxClient) UploadFile(ctx context.Context, node, storage, content, filename string,
	body io.Reader, size int64) (string, error) {
	var upid proxmox.UPID
	fields := map[string]string{"content": content, "filename": filename}
	if err := pc.Client.UploadReader(fmt.Sprintf("/nodes/%s/storage/%s/upload", node, storage),
		fields, filename, body, size, &upid); err != nil {
		return "", fmt.Errorf("uploading %s to %s on %s: %w", filename, storage, node, err)
	}
	if err := waitDiskTask(ctx, proxmox.NewTask(upid, pc.Client), diskImportTimeout); err != nil {
		return "", fmt.Errorf("uploading %s to %s on %s: %w", filename, storage, node, err)
	}
	return StorageFileVolid(storage, content, filename), nil
}

// FindStorageFile returns the volume id of the file on the storage of node
// whose name, extension aside, is filename, or "" when there is none.
func (pc *ProxmoxClient) FindStorageFile(ctx context.Context, node, storage, content, filename string) (string, error) {
	items, err := pc.GetStorageContent(ctx, node, storage)
	if err != nil {
		return "", err
	}
	prefix := StorageFileVolid(storage, content, filename)
	for _, item := range items {
		rest := strings.TrimPrefix(item.Volid, prefix)
		if rest != item.Volid && (rest == "" || strings.HasPrefix(rest, ".")) {
			return item.Volid, nil
		}
	}
	return "", nil
}

// DeleteStorageFile removes a file from the storage of node. A file that is
// already gone is not an error.
func (pc *ProxmoxClient) DeleteStorageFile(ctx context.Context, node, storage, volid string) error {
	n, err := pc.getNode(ctx, node)
	if err != nil {
		return err
	}
	s, err := n.Storage(ctx, storage)
	if err != nil {
		return fmt.Errorf("unable to get storage %s: %w", storage, err)
	}
	task, err := s.DeleteContent(ctx, volid)
	if err != nil {
		if errors.Is(err, proxmox.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("deleting %s: %w", volid, err)
	}
	return waitDiskTask(ctx, task, diskConfigTimeout)
}

// HasStorageFile reports whether volid is among the content of the storage
// of node.
func (pc *ProxmoxClient) HasStorageFile(ctx context.Context, node, storage, volid string) (bool, error) {
	items, err := pc.GetStorageContent(ctx, node, storage)
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if item.Volid == volid {
			return true, nil
		}
	}
	return false, nil
}
