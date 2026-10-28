package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"time"
)

const (
	cameraFrameLimit = 2 << 20
	cameraPollPeriod = time.Second
)

// cameraSource is administrator-managed configuration. It is never included
// in API responses; only a configured flag and authenticated frame are exposed.
type cameraSource struct {
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

func validCameraSource(source cameraSource) bool {
	if source.URL == "" || len(source.URL) > 2048 || len(source.Username) > 256 || len(source.Password) > 256 {
		return false
	}
	parsed, err := url.ParseRequestURI(source.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	return true
}

func (s *Server) setCameraSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !driverName.MatchString(id) {
		fail(w, http.StatusBadRequest, "invalid driver ID")
		return
	}
	var source cameraSource
	if !decode(w, r, &source) {
		return
	}
	if source.URL != "" && !validCameraSource(source) {
		fail(w, http.StatusBadRequest, "camera URL must be an HTTP or HTTPS MJPEG/JPEG endpoint without embedded credentials")
		return
	}
	if source.URL == "" && (source.Username != "" || source.Password != "") {
		fail(w, http.StatusBadRequest, "camera credentials require a source URL")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.storageErr != nil {
		fail(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	next := make(map[string]cameraSource, len(s.cameras)+1)
	for driverID, configured := range s.cameras {
		next[driverID] = configured
	}
	if source.URL == "" {
		delete(next, id)
		delete(s.cameraLastGet, id)
	} else {
		next[id] = source
	}
	published, err := durableJSON(filepath.Join(s.dir, "cameras.json"), next)
	if published && err != nil {
		s.failStorage(err)
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "could not persist camera source")
		return
	}
	s.cameras = next
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getCameraFrame(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !driverName.MatchString(id) {
		fail(w, http.StatusBadRequest, "invalid driver ID")
		return
	}
	s.mu.Lock()
	source, ok := s.cameras[id]
	if !ok {
		s.mu.Unlock()
		fail(w, http.StatusNotFound, "camera is not configured for this driver")
		return
	}
	now := time.Now()
	if last := s.cameraLastGet[id]; !last.IsZero() && now.Sub(last) < cameraPollPeriod {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		fail(w, http.StatusTooManyRequests, "camera frame rate is limited to one request per second")
		return
	}
	s.cameraLastGet[id] = now
	s.mu.Unlock()

	frame, err := fetchCameraFrame(r.Context(), source)
	if err != nil {
		fail(w, http.StatusBadGateway, "camera offline or stale")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(frame)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(frame)
}

func fetchCameraFrame(ctx context.Context, source cameraSource) ([]byte, error) {
	parsed, err := url.Parse(source.URL)
	if err != nil || !validCameraSource(source) {
		return nil, errors.New("invalid camera source")
	}
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: time.Second}).DialContext,
		TLSHandshakeTimeout:   time.Second,
		ResponseHeaderTimeout: 2 * time.Second,
		MaxIdleConns:          2,
		IdleConnTimeout:       5 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "multipart/x-mixed-replace, image/jpeg")
	if source.Username != "" || source.Password != "" {
		request.SetBasicAuth(source.Username, source.Password)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("camera returned a non-success status")
	}
	mediaType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	var frame []byte
	switch mediaType {
	case "image/jpeg":
		frame, err = io.ReadAll(io.LimitReader(response.Body, cameraFrameLimit+1))
	case "multipart/x-mixed-replace":
		boundary := params["boundary"]
		if boundary == "" || len(boundary) > 70 {
			return nil, errors.New("camera returned an invalid MJPEG boundary")
		}
		frame, err = firstJPEGFrame(multipart.NewReader(response.Body, boundary))
	default:
		return nil, errors.New("camera did not return JPEG or MJPEG")
	}
	if err != nil || !validJPEG(frame) {
		return nil, errors.New("camera frame is invalid or exceeds the size limit")
	}
	return frame, nil
}

func firstJPEGFrame(reader *multipart.Reader) ([]byte, error) {
	for {
		part, err := reader.NextPart()
		if err != nil {
			return nil, err
		}
		mediaType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil {
			return nil, err
		}
		if mediaType != "image/jpeg" {
			return nil, errors.New("camera MJPEG stream did not start with a JPEG frame")
		}
		frame, err := io.ReadAll(io.LimitReader(part, cameraFrameLimit+1))
		return frame, err
	}
}

func validJPEG(frame []byte) bool {
	return len(frame) >= 4 && len(frame) <= cameraFrameLimit && bytes.HasPrefix(frame, []byte{0xff, 0xd8}) && bytes.HasSuffix(frame, []byte{0xff, 0xd9})
}
