package webrpc

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeUploader records what the endpoint handed it.
type fakeUploader struct {
	calls []struct {
		host, dir, name, body string
	}
}

func (f *fakeUploader) UploadFile(hostID, remoteDir, name string, r io.Reader) (int64, error) {
	b, _ := io.ReadAll(r)
	f.calls = append(f.calls, struct{ host, dir, name, body string }{hostID, remoteDir, name, string(b)})
	return int64(len(b)), nil
}

func uploadServer(t *testing.T) (*httptest.Server, *fakeUploader) {
	t.Helper()
	up := &fakeUploader{}
	s := NewServer(New(&fakeApp{}), "")
	s.SetUploader(up)
	srv := httptest.NewServer(s.Handler(nil))
	t.Cleanup(srv.Close)
	return srv, up
}

func multipartBody(t *testing.T, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, body := range files {
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte(body))
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestUploadEndpointStreamsToTheUploader(t *testing.T) {
	srv, up := uploadServer(t)
	body, ct := multipartBody(t, map[string]string{"a.txt": "hello"})

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/upload?hostId=h1&dir=/tmp/x", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Origin", srv.URL)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	if len(up.calls) != 1 {
		t.Fatalf("uploader called %d times, want 1", len(up.calls))
	}
	c := up.calls[0]
	if c.host != "h1" || c.dir != "/tmp/x" || c.name != "a.txt" || c.body != "hello" {
		t.Errorf("uploader got %+v", c)
	}
}

// /upload writes to a production server, so it needs the same origin defence as
// /rpc — a WS-less multipart POST from a foreign page must not reach SFTP.
func TestUploadEndpointRejectsForeignOrigin(t *testing.T) {
	srv, up := uploadServer(t)
	body, ct := multipartBody(t, map[string]string{"a.txt": "x"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/upload?hostId=h1&dir=/tmp", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Origin", "https://evil.example.com")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want 403", res.StatusCode)
	}
	if len(up.calls) != 0 {
		t.Error("a foreign-origin upload reached the uploader")
	}
}

func TestUploadEndpointRequiresHostAndDir(t *testing.T) {
	srv, _ := uploadServer(t)
	body, ct := multipartBody(t, map[string]string{"a.txt": "x"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/upload", body) // no query
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Origin", srv.URL)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", res.StatusCode)
	}
}

// With no uploader set, /upload is simply not registered (404), not a broken
// 500 — a deployment that does not want uploads just does not get the route.
func TestUploadNotRegisteredWithoutUploader(t *testing.T) {
	s := NewServer(New(&fakeApp{}), "")
	srv := httptest.NewServer(s.Handler(nil))
	defer srv.Close()
	body, ct := multipartBody(t, map[string]string{"a.txt": "x"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/upload?hostId=h&dir=/tmp", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Origin", srv.URL)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", res.StatusCode)
	}
}
