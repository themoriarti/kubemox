package proxmox

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luthermonson/go-proxmox"
)

func TestImportFilename(t *testing.T) {
	cases := map[string]string{
		"https://cloud-images.example.org/noble/noble-server-cloudimg-amd64.img?x=1": "vmvol-tenant-a-web.img",
		"https://example.org/debian-13.qcow2":                                        "vmvol-tenant-a-web.qcow2",
		"https://example.org/Win11.ISO":                                              "vmvol-tenant-a-web.iso",
		"https://example.org/download/12345":                                         "vmvol-tenant-a-web.raw",
		"":                                                                           "vmvol-tenant-a-web.raw",
	}
	for src, want := range cases {
		if got := ImportFilename(testNamespace, "web", src); got != want {
			t.Errorf("ImportFilename(%q) = %q, want %q", src, got, want)
		}
	}
}

// fakeStorageAPI serves the storage slice of the API: a content listing, an
// upload that records what it received and answers with a task, a delete,
// and the task status every call waits on.
type fakeStorageAPI struct {
	content  []map[string]any
	uploaded map[string]int64 // filename -> bytes received
	deleted  []string
}

func (f *fakeStorageAPI) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"version": "9.2.20", "release": "9.2"})
	})
	mux.HandleFunc("/api2/json/nodes/pve1/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{})
	})
	mux.HandleFunc("/api2/json/nodes/pve1/storage/local/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"storage": "local", "content": "iso,import"})
	})
	mux.HandleFunc("/api2/json/nodes/pve1/storage/local/content", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, f.content)
	})
	mux.HandleFunc("/api2/json/nodes/pve1/storage/local/content/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeError(w, 405, "method")
			return
		}
		f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, "/api2/json/nodes/pve1/storage/local/content/"))
		writeJSON(w, "UPID:pve1:0000AAAA:0000BBBB:0000CCCC:imgdel:0:root@pam:")
	})
	mux.HandleFunc("/api2/json/nodes/pve1/storage/local/upload", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		file, hdr, err := r.FormFile("filename")
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		n, _ := io.Copy(io.Discard, file)
		if f.uploaded == nil {
			f.uploaded = map[string]int64{}
		}
		f.uploaded[r.FormValue("content")+"/"+hdr.Filename] = n
		writeJSON(w, "UPID:pve1:0000AAAA:0000BBBB:0000CCCC:imgcopy:0:root@pam:")
	})
	mux.HandleFunc("/api2/json/nodes/pve1/tasks/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{keyStatus: stateStopped, "exitstatus": "OK", "node": "pve1",
			"upid": strings.Split(strings.TrimPrefix(r.URL.Path, "/api2/json/nodes/pve1/tasks/"), "/")[0],
			"type": "imgcopy", "user": "root@pam", "id": "0"})
	})
	return mux
}

func storageTestClient(t *testing.T, h http.Handler) *ProxmoxClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	client := proxmox.NewClient(srv.URL+"/api2/json",
		proxmox.WithAPIToken("test@pve!t", "secret"),
		proxmox.WithHTTPClient(srv.Client()))
	return &ProxmoxClient{Client: client, nodesCache: make(map[string]NodeCache)}
}

func TestUploadFromURLStreamsAndSpools(t *testing.T) {
	image := strings.Repeat("x", 4096)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chunked.qcow2" {
			// No Content-Length: the operator has to spool before uploading.
			w.Header().Set("Transfer-Encoding", "chunked")
			flusher, _ := w.(http.Flusher)
			_, _ = io.WriteString(w, image[:2048])
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = io.WriteString(w, image[2048:])
			return
		}
		if r.URL.Path == "/missing.qcow2" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, image)
	}))
	t.Cleanup(src.Close)

	f := &fakeStorageAPI{}
	pc := storageTestClient(t, f.handler(t))
	ctx := context.Background()

	volid, err := pc.UploadFromURL(ctx, fakeNode, "local", ContentImport, "vmvol-a-b.qcow2", src.URL+"/sized.qcow2")
	if err != nil {
		t.Fatalf("UploadFromURL: %v", err)
	}
	if volid != "local:import/vmvol-a-b.qcow2" || f.uploaded["import/vmvol-a-b.qcow2"] != int64(len(image)) {
		t.Fatalf("volid %q, uploaded %v", volid, f.uploaded)
	}
	if _, err = pc.UploadFromURL(ctx, fakeNode, "local", ContentISO, "vmvol-a-c.iso", src.URL+"/chunked.qcow2"); err != nil {
		t.Fatalf("UploadFromURL chunked: %v", err)
	}
	if f.uploaded["iso/vmvol-a-c.iso"] != int64(len(image)) {
		t.Fatalf("spooled upload received %d bytes", f.uploaded["iso/vmvol-a-c.iso"])
	}
	if _, err = pc.UploadFromURL(ctx, fakeNode, "local", ContentImport, "x.qcow2", src.URL+"/missing.qcow2"); err == nil ||
		!strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("missing source: err = %v", err)
	}
}

func TestFindHasAndDeleteStorageFile(t *testing.T) {
	f := &fakeStorageAPI{content: []map[string]any{
		{"volid": "local:import/other.qcow2", "format": "qcow2", "size": 1},
		{"volid": "local:import/vmvol-tenant-a-web.vmdk", "format": "vmdk", "size": 1},
		{"volid": "local:import/vmvol-tenant-a-webserver.qcow2", "format": "qcow2", "size": 1},
	}}
	pc := storageTestClient(t, f.handler(t))
	ctx := context.Background()

	got, err := pc.FindStorageFile(ctx, fakeNode, "local", ContentImport, "vmvol-tenant-a-web")
	if err != nil || got != "local:import/vmvol-tenant-a-web.vmdk" {
		t.Fatalf("FindStorageFile = %q, %v", got, err)
	}
	if got, err = pc.FindStorageFile(ctx, fakeNode, "local", ContentImport, "vmvol-tenant-a-db"); err != nil || got != "" {
		t.Fatalf("FindStorageFile(absent) = %q, %v", got, err)
	}
	present, err := pc.HasStorageFile(ctx, fakeNode, "local", "local:import/other.qcow2")
	if err != nil || !present {
		t.Fatalf("HasStorageFile = %v, %v", present, err)
	}
	if err = pc.DeleteStorageFile(ctx, fakeNode, "local", "local:import/other.qcow2"); err != nil {
		t.Fatalf("DeleteStorageFile: %v", err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "local:import/other.qcow2" {
		t.Fatalf("deleted %v", f.deleted)
	}
}
